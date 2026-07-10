package segments

import (
	"errors"
	"testing"

	"github.com/jandedobbeleer/oh-my-posh/src/runtime"
	runtimemock "github.com/jandedobbeleer/oh-my-posh/src/runtime/mock"
	"github.com/jandedobbeleer/oh-my-posh/src/segments/options"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitlabProjectPath(t *testing.T) {
	cases := []struct {
		Case     string
		Remote   string
		Expected string
		Error    error
	}{
		{Case: "HTTPS", Remote: "https://gitlab.com/group/project.git", Expected: "group/project"},
		{Case: "Nested HTTPS", Remote: "https://gitlab.com/group/subgroup/project.git", Expected: "group/subgroup/project"},
		{Case: "SSH", Remote: "git@gitlab.com:group/project.git", Expected: "group/project"},
		{Case: "SSH URL", Remote: "ssh://git@gitlab.com/group/project.git", Expected: "group/project"},
		{Case: "Not GitLab", Remote: "git@github.com:group/project.git", Error: errNoGitlabRemote},
	}

	for _, tc := range cases {
		t.Run(tc.Case, func(t *testing.T) {
			actual, err := gitlabProjectPath(tc.Remote)

			if tc.Error != nil {
				require.ErrorIs(t, err, tc.Error)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.Expected, actual)
		})
	}
}

func TestGitlabPipelineCacheKey(t *testing.T) {
	assert.Equal(
		t,
		"gitlab_ci|group/project|main|abc123",
		gitlabPipelineCacheKey("group/project", "main", "abc123"),
	)
}

func TestGitlabCiCacheKey(t *testing.T) {
	env := new(runtimemock.Environment)
	env.On("HasParentFilePath", ".git", true).Return(&runtime.FileInfo{Path: "/repo/.git", IsDir: true}, nil)
	env.On("InWSLSharedDrive").Return(false)
	env.On("GOOS").Return(runtime.DARWIN)
	env.On("HasCommand", GITCOMMAND).Return(true)
	env.On("HasFileInParentDirs", ".gitlab-ci.yml", uint(10)).Return(true)
	env.On("RunCommand", GITCOMMAND, []string{"-C", "/repo", "--no-optional-locks", "-c", "core.quotepath=false", "-c", "color.status=false", "rev-parse", "--abbrev-ref", "HEAD"}).Return("main", nil)
	env.On("RunCommand", GITCOMMAND, []string{"-C", "/repo", "--no-optional-locks", "-c", "core.quotepath=false", "-c", "color.status=false", "rev-parse", "HEAD"}).Return("abc123", nil)
	env.On("FileContent", "/repo/.git/config").Return("[remote \"origin\"]\n\turl = git@gitlab.com:group/project.git")

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{}, env)

	key, ok := gitlabCi.CacheKey()

	require.True(t, ok)
	assert.Equal(t, "gitlab_ci|group/project|main|abc123", key)
}

func TestGitlabCiGetPipelineDataFromAPI(t *testing.T) {
	inner := new(runtimemock.Environment)
	inner.On("Getenv", gitlabTokenEnv).Return("token")
	inner.On("HTTPRequest", "https://gitlab.com/api/v4/projects/group%2Fproject/pipelines?ref=feature%2Fbranch&limit=1&per_page=1&page=1").
		Return([]byte(`[{"id":123,"status":"success","ref":"feature/branch"}]`), nil)
	env := &timeoutCapturingEnv{Environment: inner}

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{}, env)

	pipeline, err := gitlabCi.getPipelineDataFromAPI("group/project", "feature/branch")

	require.NoError(t, err)
	assert.Equal(t, 123, pipeline.ID)
	assert.Equal(t, "success", pipeline.Status)
	assert.Equal(t, gitlabAPIDefaultHTTPTimeout, env.capturedTimeout)
}

func TestGitlabCiGetPipelineDataFromAPIHTTPTimeout(t *testing.T) {
	inner := new(runtimemock.Environment)
	inner.On("Getenv", gitlabTokenEnv).Return("")
	inner.On("HTTPRequest", "https://gitlab.com/api/v4/projects/group%2Fproject/pipelines?ref=main&limit=1&per_page=1&page=1").
		Return([]byte(`[{"id":123,"status":"success"}]`), nil)
	env := &timeoutCapturingEnv{Environment: inner}

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{options.HTTPTimeout: 2500}, env)

	_, err := gitlabCi.getPipelineDataFromAPI("group/project", "main")

	require.NoError(t, err)
	assert.Equal(t, 2500, env.capturedTimeout)
}

func TestGitlabCiGetPipelineDataFromAPIEmptyResponse(t *testing.T) {
	env := new(runtimemock.Environment)
	env.On("Getenv", gitlabTokenEnv).Return("")
	env.On("HTTPRequest", "https://gitlab.com/api/v4/projects/group%2Fproject/pipelines?ref=main&limit=1&per_page=1&page=1").
		Return([]byte(`[]`), nil)

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{}, env)

	_, err := gitlabCi.getPipelineDataFromAPI("group/project", "main")

	require.ErrorIs(t, err, errNoPipeline)
}

func TestGitlabCiGetPipelineDataFromAPIInvalidJSON(t *testing.T) {
	env := new(runtimemock.Environment)
	env.On("Getenv", gitlabTokenEnv).Return("")
	env.On("HTTPRequest", "https://gitlab.com/api/v4/projects/group%2Fproject/pipelines?ref=main&limit=1&per_page=1&page=1").
		Return([]byte(`{}`), nil)

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{}, env)

	_, err := gitlabCi.getPipelineDataFromAPI("group/project", "main")

	require.Error(t, err)
}

func TestGitlabCiGetPipelineDataFromGlabForwardsToken(t *testing.T) {
	env := new(runtimemock.Environment)
	env.On("Getenv", gitlabTokenEnv).Return("token")
	env.On(
		"RunCommandWithEnv",
		glabCommand,
		[]string{"GITLAB_TOKEN=token"},
		[]string{"ci", "status", "--repo", "group/project", "--branch", "main", "--output", "json"},
	).Return(`{"id":456,"status":"running"}`, nil)

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{}, env)

	pipeline, err := gitlabCi.getPipelineDataFromGlab("group/project", "main")

	require.NoError(t, err)
	assert.Equal(t, 456, pipeline.ID)
	assert.Equal(t, "running", pipeline.Status)
}

func TestGitlabCiGetPipelineDataFromGlabWithoutToken(t *testing.T) {
	env := new(runtimemock.Environment)
	env.On("Getenv", gitlabTokenEnv).Return("")
	env.On(
		"RunCommand",
		glabCommand,
		[]string{"ci", "status", "--repo", "group/project", "--branch", "main", "--output", "json"},
	).Return(`{"id":456,"status":"running"}`, nil)

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{}, env)

	pipeline, err := gitlabCi.getPipelineDataFromGlab("group/project", "main")

	require.NoError(t, err)
	assert.Equal(t, 456, pipeline.ID)
	assert.Equal(t, "running", pipeline.Status)
}

func TestGitlabCiGetPipelineDataFromGlabCommandError(t *testing.T) {
	expectedErr := errors.New("run glab auth login")
	env := new(runtimemock.Environment)
	env.On("Getenv", gitlabTokenEnv).Return("")
	env.On(
		"RunCommand",
		glabCommand,
		[]string{"ci", "status", "--repo", "group/project", "--branch", "main", "--output", "json"},
	).Return("", expectedErr)

	gitlabCi := &GitlabCi{}
	gitlabCi.Init(options.Map{}, env)

	_, err := gitlabCi.getPipelineDataFromGlab("group/project", "main")

	require.ErrorIs(t, err, expectedErr)
}

func TestParseGlabPipelineInfo(t *testing.T) {
	cases := []struct {
		Case           string
		Output         []byte
		ExpectedID     int
		ExpectedStatus string
		Error          error
	}{
		{Case: "Pipeline", Output: []byte(`{"id":123,"status":"success"}`), ExpectedID: 123, ExpectedStatus: "success"},
		{Case: "Nested Pipeline", Output: []byte(`{"pipeline":{"id":123,"status":"success"}}`), ExpectedID: 123, ExpectedStatus: "success"},
		{Case: "String ID", Output: []byte(`{"id":"123","status":"success"}`), ExpectedID: 123, ExpectedStatus: "success"},
		{Case: "Array", Output: []byte(`[{"id":123,"status":"success"}]`), ExpectedID: 123, ExpectedStatus: "success"},
		{Case: "Empty", Output: []byte(`{}`), Error: errNoPipeline},
	}

	for _, tc := range cases {
		t.Run(tc.Case, func(t *testing.T) {
			pipeline, err := parseGlabPipelineInfo(tc.Output)

			if tc.Error != nil {
				require.ErrorIs(t, err, tc.Error)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.ExpectedID, pipeline.ID)
			assert.Equal(t, tc.ExpectedStatus, pipeline.Status)
		})
	}
}
