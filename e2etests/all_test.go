//go:build e2etests

package e2etests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gobuffalo/envy"
	"github.com/stretchr/testify/suite"
)

type E2eSuite struct {
	suite.Suite
	goBinaryPath   string
	env            []string
	goPath         string
	sampleRepoPath string
	stopAthens     context.CancelFunc
}

type catalogRes struct {
	Modules []struct {
		Module  string `json:"module"`
		Version string `json:"version"`
	} `json:"modules"`
}

func (m *E2eSuite) SetupSuite() {
	var err error
	m.goPath, err = os.MkdirTemp("/tmp", "gopath")
	m.Require().NoError(err, "failed to make temp dir")

	m.sampleRepoPath, err = os.MkdirTemp("/tmp", "repopath")
	m.Require().NoError(err, "failed to make temp dir for sample repo")

	m.goBinaryPath = envy.Get("GO_BINARY_PATH", "go")

	athensBin, err := buildAthens(m.goBinaryPath, m.goPath, m.env)
	m.Require().NoError(err, "failed to build athens")
	_ = stopAthens() // in case a dangling instance was around.

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAthens = cancel
	m.Require().NoError(runAthensAndWait(ctx, athensBin, m.getEnv()), "failed to start athens")
	m.Require().NoError(setupTestRepo(m.sampleRepoPath, "https://github.com/athens-artifacts/happy-path.git"), "failed to setup test repo")
}

func (m *E2eSuite) TearDownSuite() {
	if m.stopAthens != nil {
		m.stopAthens()
	}
	if m.goPath != "" {
		_ = chmodR(m.goPath, 0o777)
		_ = os.RemoveAll(m.goPath)
	}
	if m.sampleRepoPath != "" {
		_ = chmodR(m.sampleRepoPath, 0o777)
		_ = os.RemoveAll(m.sampleRepoPath)
	}
}

func TestE2E(t *testing.T) {
	suite.Run(t, &E2eSuite{})
}

func (m *E2eSuite) SetupTest() {
	_ = chmodR(m.goPath, 0o777)
	err := cleanGoCache(m.getEnv())
	m.Require().NoError(err, "failed to clear go cache")
}

func (m *E2eSuite) TestNoGoProxy() {
	cmd := exec.Command("go", "run", ".")
	cmd.Env = m.env
	cmd.Dir = m.sampleRepoPath

	output, err := cmd.CombinedOutput()
	m.Require().NoError(err, "go run failed on test repo: %s", string(output))
}

func (m *E2eSuite) TestGoProxy() {
	cmd := exec.Command("go", "run", ".")
	cmd.Env = m.getEnvGoProxy(m.goPath)
	cmd.Dir = m.sampleRepoPath
	output, err := cmd.CombinedOutput()
	m.Require().NoError(err, "go run failed on test repo: %s", string(output))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:3000/catalog", nil)
	m.Require().NoError(err, "failed to build catalog request")
	resp, err := http.DefaultClient.Do(req)
	m.Require().NoError(err, "failed to read catalog")
	defer resp.Body.Close()
	m.Require().Equal(http.StatusOK, resp.StatusCode)

	var catalog catalogRes
	err = json.NewDecoder(resp.Body).Decode(&catalog)
	m.Require().NoError(err, "failed to decode catalog res")
	m.Require().Len(catalog.Modules, 1)
	m.Assert().Equal("github.com/athens-artifacts/no-tags", catalog.Modules[0].Module)
}

func (m *E2eSuite) TestWrongGoProxy() {
	cmd := exec.Command("go", "run", ".")
	cmd.Env = m.getEnvWrongGoProxy(m.goPath)
	cmd.Dir = m.sampleRepoPath
	err := cmd.Run()
	m.Assert().NotNil(err, "Wrong proxy should fail")
}

func (m *E2eSuite) getEnv() []string {
	res := []string{
		fmt.Sprintf("GOPATH=%s", m.goPath),
		"GO111MODULE=on",
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		fmt.Sprintf("GOCACHE=%s", filepath.Join(m.goPath, "cache")),
	}
	return res
}

func (m *E2eSuite) getEnvGoProxy(gopath string) []string {
	res := m.getEnv()
	res = append(res, "GOPROXY=http://localhost:3000")
	return res
}

func (m *E2eSuite) getEnvWrongGoProxy(gopath string) []string {
	res := m.getEnv()
	res = append(res, "GOPROXY=http://localhost:3001")
	return res
}
