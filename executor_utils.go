package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Nitro/sidecar-executor/container"
	"github.com/fsouza/go-dockerclient"
	log "github.com/sirupsen/logrus"
)

// copyLogs will copy the Docker container logs to stdout and stderr so we can
// capture some failure information in the Mesos logs. Then tooling can fetch
// crash info from the Mesos API.
func (exec *sidecarExecutor) copyLogs(containerId string) {
	startTimeEpoch := time.Now().UTC().Add(0 - exec.config.LogsSince).Unix()

	container.GetLogs(
		exec.client, containerId, startTimeEpoch, os.Stdout, os.Stderr,
	)
}

// handleContainerLogs will, if configured to do it, watch and relay container
// logs to syslog.
func (exec *sidecarExecutor) handleContainerLogs(containerId string,
	labels map[string]string) {

	if exec.config.RelaySyslog || exec.config.RelaySyslogStartupOnly {
		var output io.Writer
		if exec.config.ContainerLogsStdout {
			output = os.Stdout
		} else {
			output = ioutil.Discard
		}

		exec.logsQuitChan = make(chan struct{})
		go exec.relayLogs(exec.logsQuitChan, containerId, labels, output)
	}
}

// getMasterHostname talks to the local worker endpoint and discovers the
// Mesos master hostname.
func (exec *sidecarExecutor) getMasterHostname() (string, error) {
	envEndpoint := os.Getenv("MESOS_AGENT_ENDPOINT")

	if len(envEndpoint) < 1 { // Did we get anything in the env var?
		return "", fmt.Errorf("Can't get MESOS_AGENT_ENDPOINT from env! Won't provide Sidecar seeds.")
	}
	localEndpoint := "http://" + envEndpoint + "/state"

	localStruct := struct {
		MasterHostname string `json:"master_hostname"`
	}{}

	// Let's find out the Mesos master's hostname
	resp, err := exec.fetcher.Get(localEndpoint)
	if err != nil {
		return "", fmt.Errorf("Unable to fetch Mesos master info from worker endpoint: %s", err)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("Error reading response body from Mesos worker! '%s'", err)
	}

	err = json.Unmarshal(body, &localStruct)
	if err != nil {
		return "", fmt.Errorf("Error parsing response body from Mesos worker! '%s'", err)
	}

	return localStruct.MasterHostname, nil
}

// getWorkerHostnames returns a slice of all the current worker hostnames
func (exec *sidecarExecutor) getWorkerHostnames(masterHostname string) ([]string, error) {
	masterAddr := masterHostname
	if exec.config.MesosMasterPort != "" {
		masterAddr += ":" + exec.config.MesosMasterPort
	}
	masterEndpoint := "http://" + masterAddr + "/slaves"

	type workersStruct struct {
		Hostname string `json:"hostname"`
	}

	masterStruct := struct {
		Slaves []workersStruct `json:"slaves"`
	}{}

	// Let's find out the Mesos master's hostname
	resp, err := exec.fetcher.Get(masterEndpoint)
	if err != nil {
		return nil, fmt.Errorf("Unable to fetch info from master endpoint: %s", err)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Error reading response body from Mesos master! '%s'", err)
	}

	err = json.Unmarshal(body, &masterStruct)
	if err != nil {
		return nil, fmt.Errorf("Error parsing response body from Mesos master! '%s'", err)
	}

	var workers []string
	for _, worker := range masterStruct.Slaves {
		workers = append(workers, worker.Hostname)
	}

	return workers, nil
}

// addSidecarSeeds mutates the passed slice and inserts an env var formatted
// string (FOO=BAR_1) containing the list of Sidecar seeds that should be
// used to bootstrap a Sidecar instance.
func (exec *sidecarExecutor) addSidecarSeeds(envVars []string) []string {
	masterHostname, err := exec.getMasterHostname()
	if err != nil {
		log.Error(err.Error())
		return envVars
	}

	workerNames, err := exec.getWorkerHostnames(masterHostname)
	if err != nil {
		log.Error(err.Error())
		return envVars
	}

	return append(envVars, "SIDECAR_SEEDS="+strings.Join(workerNames, ","))
}

// notifyDrain instructs Sidecar to set the current service's status to DRAINING
func (exec *sidecarExecutor) notifyDrain() {
	// Check if draining is required
	if !shouldCheckSidecar(exec.containerConfig) ||
		exec.config.SidecarDrainingDuration == 0 {
		return
	}

	// NB: Unfortunately, since exec.config.SidecarUrl points to
	// `state.json`, we need to extract the Host from it first.
	sidecarUrl, err := url.Parse(exec.config.SidecarUrl)
	if err != nil {
		log.Errorf("Error parsing Sidercar URL: %s", err)
		return
	}

	if exec.containerID == "" {
		log.Error("Attempted to drain service with empty container ID")
		return
	}

	// URL.Host contains the port as well, if present
	sidecarDrainServiceUrl := url.URL{
		Scheme: sidecarUrl.Scheme,
		Host:   sidecarUrl.Host,
		Path:   fmt.Sprintf("/api/services/%s/drain", exec.containerID[:12]),
	}

	drainer := func() (int, error) {
		resp, err := exec.fetcher.Post(sidecarDrainServiceUrl.String(), "", nil)
		if err != nil {
			return 0, err
		}

		defer resp.Body.Close()

		return resp.StatusCode, nil
	}

	log.Warnf("Setting service ID %q status to DRAINING in Sidecar", exec.containerID[:12])

	// Bridge the watcher waitgroup to a channel
	watcherDoneChan := make(chan struct{})
	go func() {
		exec.watcherWg.Wait()
		close(watcherDoneChan)
	}()

RETRIES:
	// Try several times to instruct Sidecar to set this service to DRAINING
	for i := 0; i <= exec.config.SidecarRetryCount; i++ {
		status, err := drainer()

		if err == nil && status == 202 {
			break
		}

		log.Warnf("Failed %d attempts to set service to DRAINING in Sidecar!", i+1)

		select {
		case <-watcherDoneChan:
			break RETRIES
		default:
		}

		time.Sleep(exec.config.SidecarRetryDelay)
	}
	ticker := time.NewTicker(exec.config.SidecarDrainingDuration)
	defer ticker.Stop()
	select {
	case <-ticker.C:
		// Finished waiting SidecarDrainingDuration
	case <-watcherDoneChan:
		// Bail out early if the watcher exits in the mean time
	}
}

// Check if it should check Sidecar status, assuming enabled by default
func shouldCheckSidecar(containerConfig *docker.CreateContainerOptions) bool {
	value, ok := containerConfig.Config.Labels["SidecarDiscover"]
	if !ok {
		return true
	}

	if enabled, err := strconv.ParseBool(value); err == nil {
		return enabled
	}

	return true
}
