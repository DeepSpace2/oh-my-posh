package segments

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jandedobbeleer/oh-my-posh/src/log"
	"github.com/jandedobbeleer/oh-my-posh/src/runtime"
	"github.com/jandedobbeleer/oh-my-posh/src/segments/options"
)

type PipelineInfo struct {
	ID        int       `json:"id"`
	Iid       int       `json:"iid"`
	ProjectID int       `json:"project_id"`
	Sha       string    `json:"sha"`
	Ref       string    `json:"ref"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
	Name      any       `json:"name"`
}

type GitlabCi struct {
	Base
	Git

	Icon  string
	Error string
	PipelineInfo
}

const (
	gitlabAPIDefaultHTTPTimeout = 1000
	gitlabTokenEnv              = "GITLAB_TOKEN"
	glabCommand                 = "glab"
)

var (
	errNoPipeline     = errors.New("no GitLab CI pipeline found")
	errNoGitlabRemote = errors.New("not a GitLab remote")
)

func (gitlabCi *GitlabCi) CacheKey() (string, bool) {
	if !gitlabCi.shouldDisplay() {
		return "", false
	}

	if !gitlabCi.env.HasFileInParentDirs(".gitlab-ci.yml", 10) {
		return "", false
	}

	branch := gitlabCi.getGitCommandOutput("rev-parse", "--abbrev-ref", "HEAD")
	commitSha := gitlabCi.getGitCommandOutput("rev-parse", "HEAD")
	remoteURL := gitlabCi.getRemoteURL()
	projectPath, err := gitlabProjectPath(remoteURL)
	if err != nil {
		return "", false
	}

	return gitlabPipelineCacheKey(projectPath, branch, commitSha), true
}

func (gitlabCi *GitlabCi) Enabled() bool {
	if !gitlabCi.shouldDisplay() {
		return false
	}

	if !gitlabCi.env.HasFileInParentDirs(".gitlab-ci.yml", 10) {
		return false
	}

	gitlabCi.Icon = gitlabCi.options.String(GitlabIcon, "\uf135")

	if err := gitlabCi.setPipelineStatus(); err != nil {
		log.Error(err)
		gitlabCi.Error = err.Error()
		return gitlabCi.options.Bool(options.DisplayError, false)
	}

	return true
}

func (gitlabCi *GitlabCi) Template() string {
	return " {{ if .Error }}<#999999,#990000>{{ .Error }}</>{{ else }}{{ .ID }} {{ .Status }}{{end}} "
}

func (gitlabCi *GitlabCi) setPipelineStatus() error {
	gitlabCi.setStatus()
	branch := gitlabCi.Git.Ref
	if branch == "" || branch == DETACHED {
		branch = gitlabCi.getGitCommandOutput("rev-parse", "--abbrev-ref", "HEAD")
	}

	remoteURL := gitlabCi.getRemoteURL()
	projectPath, err := gitlabProjectPath(remoteURL)
	if err != nil {
		return err
	}

	var pipeline PipelineInfo
	if gitlabCi.env.HasCommand(glabCommand) {
		pipeline, err = gitlabCi.getPipelineDataFromGlab(projectPath, branch)
	} else {
		pipeline, err = gitlabCi.getPipelineDataFromAPI(projectPath, branch)
	}

	if err != nil {
		return err
	}

	gitlabCi.PipelineInfo = pipeline
	return nil
}

func gitlabPipelineCacheKey(repoPath, branchName, commitSha string) string {
	return strings.Join([]string{"gitlab_ci", repoPath, branchName, commitSha}, "|")
}

func buildLatestPipelineByShaUrl(repoPath, latestBranchCommitSha string) string {
	return fmt.Sprintf(
		"https://gitlab.com/api/v4/projects/%v/pipelines?sha=%v&limit=1&per_page=1&page=1",
		url.PathEscape(repoPath),
		url.QueryEscape(latestBranchCommitSha),
	)
}

func buildLatestPipelineByBranchUrl(repoPath, branchName string) string {
	return fmt.Sprintf(
		"https://gitlab.com/api/v4/projects/%v/pipelines?ref=%v&limit=1&per_page=1&page=1",
		url.PathEscape(repoPath),
		url.QueryEscape(branchName),
	)
}

func gitlabProjectPath(remoteURL string) (string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	remoteURL = strings.TrimSuffix(remoteURL, ".git")

	if parsedURL, err := url.Parse(remoteURL); err == nil && strings.Contains(parsedURL.Host, "gitlab") {
		return strings.Trim(parsedURL.Path, "/"), nil
	}

	const gitlabSSHPrefix = "git@gitlab.com:"
	if strings.HasPrefix(remoteURL, gitlabSSHPrefix) {
		return strings.TrimPrefix(remoteURL, gitlabSSHPrefix), nil
	}

	if strings.Contains(remoteURL, "gitlab") {
		parts := strings.SplitN(remoteURL, ":", 2)
		if len(parts) == 2 {
			return strings.Trim(parts[1], "/"), nil
		}
	}

	return "", errNoGitlabRemote
}

func (gitlabCi *GitlabCi) getPipelineDataFromAPI(repoPath, branchName string) (PipelineInfo, error) {
	gitlabAPIToken := gitlabCi.env.Getenv(gitlabTokenEnv)
	addHeaders := func(request *http.Request) {
		request.Header.Set("Accept", "application/json")
		if len(gitlabAPIToken) != 0 {
			request.Header.Set("PRIVATE-TOKEN", gitlabAPIToken)
		}
	}

	requestURL := buildLatestPipelineByBranchUrl(repoPath, branchName)
	httpTimeout := gitlabCi.options.Int(options.HTTPTimeout, gitlabAPIDefaultHTTPTimeout)
	body, err := gitlabCi.env.HTTPRequest(requestURL, nil, httpTimeout, addHeaders)
	if err != nil {
		return PipelineInfo{}, err
	}

	var pipelineInfo []PipelineInfo
	if err := json.Unmarshal(body, &pipelineInfo); err != nil {
		return PipelineInfo{}, err
	}

	if len(pipelineInfo) == 0 {
		return PipelineInfo{}, errNoPipeline
	}

	return pipelineInfo[0], nil
}

func (gitlabCi *GitlabCi) getPipelineDataFromGlab(repoPath, branchName string) (PipelineInfo, error) {
	args := []string{"ci", "status", "--repo", repoPath, "--branch", branchName, "--output", "json"}
	gitlabAPIToken := gitlabCi.env.Getenv(gitlabTokenEnv)

	var (
		output string
		err    error
	)
	if len(gitlabAPIToken) == 0 {
		output, err = gitlabCi.env.RunCommand(glabCommand, args...)
	} else {
		output, err = gitlabCi.env.RunCommandWithEnv(glabCommand, []string{gitlabTokenEnv + "=" + gitlabAPIToken}, args...)
	}

	if err != nil {
		return PipelineInfo{}, err
	}

	return parseGlabPipelineInfo([]byte(output))
}

func parseGlabPipelineInfo(output []byte) (PipelineInfo, error) {
	var pipelines []PipelineInfo
	if err := json.Unmarshal(output, &pipelines); err == nil {
		if len(pipelines) == 0 {
			return PipelineInfo{}, errNoPipeline
		}
		return pipelines[0], nil
	}

	var pipelineInfo PipelineInfo
	if err := json.Unmarshal(output, &pipelineInfo); err == nil && (pipelineInfo.ID != 0 || len(pipelineInfo.Status) != 0) {
		return pipelineInfo, nil
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(output, &result); err != nil {
		return PipelineInfo{}, err
	}

	for _, key := range []string{"pipeline", "current_pipeline", "currentPipeline"} {
		if rawPipeline, ok := result[key]; ok {
			return parseGlabPipelineInfo(rawPipeline)
		}
	}

	for _, key := range []string{"id", "iid"} {
		if rawID, ok := result[key]; ok {
			pipelineInfo.ID = parseJSONInt(rawID)
		}
	}

	if rawStatus, ok := result["status"]; ok {
		_ = json.Unmarshal(rawStatus, &pipelineInfo.Status)
	}

	if pipelineInfo.ID == 0 && len(pipelineInfo.Status) == 0 {
		return PipelineInfo{}, errNoPipeline
	}

	return pipelineInfo, nil
}

func parseJSONInt(value json.RawMessage) int {
	var number int
	if err := json.Unmarshal(value, &number); err == nil {
		return number
	}

	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return 0
	}

	number, _ = strconv.Atoi(text)
	return number
}

func (gitlabCi *GitlabCi) Init(opts options.Provider, env runtime.Environment) {
	gitlabCi.Base.Init(opts, env)
	gitlabCi.Git.env = env
	gitlabCi.Git.options = opts
}
