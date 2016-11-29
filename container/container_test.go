package container

import (
	"testing"

	"github.com/fsouza/go-dockerclient"
	mesos "github.com/mesos/mesos-go/mesosproto"
	. "github.com/smartystreets/goconvey/convey"
)

type mockDockerClient struct {
	ValidOptions bool
}

func (m *mockDockerClient) PullImage(opts docker.PullImageOptions, auth docker.AuthConfiguration) error {
	if len(opts.Repository) > 5 && (docker.AuthConfiguration{}) == auth {
		m.ValidOptions = true
	}

	return nil
}

func Test_PullImage(t *testing.T) {
	Convey("PullImage() passes the right params", t, func() {
		image := "foo/foo:foo"
		taskInfo := &mesos.TaskInfo{
			Container: &mesos.ContainerInfo{
				Docker: &mesos.ContainerInfo_DockerInfo{
					Image: &image,
				},
			},
		}

		dockerClient := &mockDockerClient{}
		PullImage(dockerClient, taskInfo)

		So(dockerClient.ValidOptions, ShouldBeTrue)
	})
}

func Test_ConfigGeneration(t *testing.T) {
	Convey("Generating the Docker config from a Mesos Task", t, func() {
		taskId := "nginx-2392676-1479746266455-1-dev_singularity_sick_sing-DEFAULT"
		image := "foo/foo:foo"
		cpus := "cpus"
		cpusValue := float64(0.5)
		env := "env"
		envValue := "SOMETHING=123=123"
		port := uint32(8080)
		port2 := uint32(443)
		port2_hp := uint32(10270)
		label := "label"
		labelValue := "ANYTHING=123=123"
		v1_cp := "/tmp/somewhere"
		v1_hp := "/tmp/elsewhere"
		v2_cp := "/tmp/foo"
		v2_hp := "/tmp/bar"
		mode := mesos.Volume_RO

		taskInfo := &mesos.TaskInfo{
			TaskId: &mesos.TaskID{Value: &taskId},
			Container: &mesos.ContainerInfo{
				Docker: &mesos.ContainerInfo_DockerInfo{
					Image: &image,
					Parameters: []*mesos.Parameter{
						{
							Key:   &env,
							Value: &envValue,
						},
						{
							Key:   &label,
							Value: &labelValue,
						},
					},
					PortMappings: []*mesos.ContainerInfo_DockerInfo_PortMapping{
						{
							ContainerPort: &port,
						},
						{
							ContainerPort: &port2,
							HostPort:      &port2_hp,
						},
					},
				},
				Volumes: []*mesos.Volume{
					{
						Mode:          &mode,
						ContainerPath: &v1_cp,
						HostPath:      &v1_hp,
					},
					{
						ContainerPath: &v2_cp,
						HostPath:      &v2_hp,
					},
				},
			},
			Resources: []*mesos.Resource{
				{
					Name:   &cpus,
					Scalar: &mesos.Value_Scalar{Value: &cpusValue},
				},
			},
		}

		opts := ConfigForTask(taskInfo)

		Convey("gets the name from the task ID", func() {
			So(opts.Name, ShouldEqual, taskId)
		})

		Convey("properly calculates the CPU shares", func() {
			So(opts.Config.CPUShares, ShouldEqual, float64(512))
		})

		Convey("populates the environment", func() {
			So(len(opts.Config.Env), ShouldEqual, 1)
			So(opts.Config.Env[0], ShouldEqual, "SOMETHING=123=123")
		})

		Convey("fills in the exposed ports", func() {
			So(len(opts.Config.ExposedPorts), ShouldEqual, 2)
			So(opts.Config.ExposedPorts["8080/tcp"], ShouldNotBeNil)
		})

		Convey("has the right image name", func() {
			So(opts.Config.Image, ShouldEqual, image)
		})

		Convey("gets the labels", func() {
			So(len(opts.Config.Labels), ShouldEqual, 1)
			So(opts.Config.Labels["ANYTHING"], ShouldEqual, "123=123")
		})

		Convey("grabs and formats volume binds properly", func() {
			So(len(opts.HostConfig.Binds), ShouldEqual, 2)
			So(opts.HostConfig.Binds[0], ShouldEqual, "/tmp/elsewhere:/tmp/somewhere:ro")
			So(opts.HostConfig.Binds[1], ShouldEqual, "/tmp/bar:/tmp/foo")
		})

		Convey("handles port bindings", func() {
			So(len(opts.HostConfig.PortBindings), ShouldEqual, 1)
			So(opts.HostConfig.PortBindings["443/tcp"][0].HostPort, ShouldEqual, "10270")
		})
	})
}