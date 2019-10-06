package vault

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	. "github.com/smartystreets/goconvey/convey"
)

func Test_DecryptEnvs(t *testing.T) {
	envVault := EnvVault{client: &mockVaultAPI{}}

	Convey("When working with EnvVault", t, func() {
		Reset(func() { log.SetOutput(ioutil.Discard) })
		Convey("isVaultPath() returns false for a normal env value", func() {
			value := isVaultPath("value")
			So(value, ShouldBeFalse)

			value = isVaultPath("1234")
			So(value, ShouldBeFalse)

			value = isVaultPath("http://url")
			So(value, ShouldBeFalse)

		})

		Convey("isVaultPath() returns true for a vault key value", func() {
			value := isVaultPath("vault://test")
			So(value, ShouldBeTrue)

			value = isVaultPath("vault://secret/key1")
			So(value, ShouldBeTrue)
		})

		Convey("It processes all environment vars with Vault path", func() {
			envs := []string{"Key1=Value1", "Key2=Value2", "Key3=vault://secure/validKey"}
			expected := []string{"Key1=Value1", "Key2=Value2", "Key3=secret_value"}
			securedValue, err := envVault.DecryptAllEnv(envs)
			So(err, ShouldBeNil)
			So(securedValue, ShouldResemble, expected)
		})

		Convey("ReadsecretValue", func() {
			Convey("Fails for an invalid key", func() {
				value, err := envVault.ReadSecretValue("invalid path")
				So(value, ShouldBeEmpty)
				So(err.Error(), ShouldContainSubstring, "invalid path")

			})

			Convey("Fails for key not found", func() {
				value, err := envVault.ReadSecretValue("vault://secure/notFoundKey")
				So(value, ShouldBeEmpty)
				So(err.Error(), ShouldContainSubstring, "Path 'secure/notFoundKey' not found")
			})

			Convey("Returns a secured value for a valid key", func() {
				securedValue, err := envVault.ReadSecretValue("vault://secure/validKey")
				So(err, ShouldBeNil)
				So(securedValue, ShouldEqual, "secret_value")

			})

			Convey("Returns a secured value for a valid sub-key", func() {
				securedValue, err := envVault.ReadSecretValue("vault://secure/validKeyWithSub?key=subkey")
				So(err, ShouldBeNil)
				So(securedValue, ShouldEqual, "some_sub_value")
			})

			Convey("Returns an error on a missing sub-key", func() {
				securedValue, err := envVault.ReadSecretValue("vault://secure/validKeyWithSub?key=missing")
				So(err.Error(), ShouldContainSubstring, "Value for path 'secure/validKeyWithSub' not found")
				So(securedValue, ShouldEqual, "")
			})
		})
	})
}

type mockVaultAPI struct {
	VaultAPI
}

func (m mockVaultAPI) Address() string {
	return "http://test"
}

func (m mockVaultAPI) NewRequest(method, path string) *api.Request {
	url, _ := url.Parse(path)
	return &api.Request{URL: url}
}

func (m mockVaultAPI) RawRequest(r *api.Request) (*api.Response, error) {
	if strings.Contains(r.URL.Path, "secure/notFoundKey") {
		return &api.Response{
			Response: &http.Response{
				StatusCode: 404,
				Body:       ioutil.NopCloser(bytes.NewReader([]byte(""))),
			},
		}, nil
	}

	if strings.Contains(r.URL.Path, "secure/validKeyWithSub") {
		return &api.Response{
			Response: &http.Response{
				StatusCode: 200,
				Body:       ioutil.NopCloser(bytes.NewReader([]byte(`{ "data":{ "subkey": "some_sub_value" }}`))),
			},
		}, nil
	}

	if strings.Contains(r.URL.Path, "secure/validKey") {
		return &api.Response{
			Response: &http.Response{
				StatusCode: 200,
				Body:       ioutil.NopCloser(bytes.NewReader([]byte(`{ "data":{ "value": "secret_value" }}`))),
			},
		}, nil
	}

	return nil, errors.New("Unexpected")
}
