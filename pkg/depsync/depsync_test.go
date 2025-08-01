//go:build unit
// +build unit

package depsync

import (
	"context"
	"testing"

	"github.com/cryptellation/depsync/pkg/adapters/dagger"
	"github.com/cryptellation/depsync/pkg/adapters/github"
	"github.com/cryptellation/depsync/pkg/config"
	"github.com/cryptellation/depsync/pkg/depgraph"
	"github.com/cryptellation/depsync/pkg/repo"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

// TestDepSync contains all the mocks and the depsync instance for testing
type TestDepSync struct {
	DepSync             *DepSync
	MockController      *gomock.Controller
	MockFetcher         *repo.MockFilesFetcher
	MockGraphBuilder    *depgraph.MockGraphBuilder
	MockVersionDetector *repo.MockVersionDetector
	MockChecker         *depgraph.MockInconsistencyChecker
	MockDagger          *dagger.MockDagger
	MockGitHubClient    *github.MockClient
}

// newTestDepSync creates a TestDepSync instance with all mocked dependencies
func newTestDepSync(t *testing.T, cfg *config.Config) *TestDepSync {
	ctrl := gomock.NewController(t)

	// Create all mocks
	mockFetcher := repo.NewMockFilesFetcher(ctrl)
	mockGraphBuilder := depgraph.NewMockGraphBuilder(ctrl)
	mockVersionDetector := repo.NewMockVersionDetector(ctrl)
	mockChecker := depgraph.NewMockInconsistencyChecker(ctrl)
	mockDagger := dagger.NewMockDagger(ctrl)
	mockGitHubClient := github.NewMockClient(ctrl)

	// Set up default expectations
	mockDagger.EXPECT().Close().Return(nil)

	// Create DepSync directly, avoiding New() which requires Docker
	c := &DepSync{
		config:          cfg,
		client:          mockGitHubClient,
		fetcher:         mockFetcher,
		graphBuilder:    mockGraphBuilder,
		versionDetector: mockVersionDetector,
		checker:         mockChecker,
		dagger:          mockDagger,
	}

	return &TestDepSync{
		DepSync:             c,
		MockController:      ctrl,
		MockFetcher:         mockFetcher,
		MockGraphBuilder:    mockGraphBuilder,
		MockVersionDetector: mockVersionDetector,
		MockChecker:         mockChecker,
		MockDagger:          mockDagger,
		MockGitHubClient:    mockGitHubClient,
	}
}

func TestDepSync_Run_NoRepositories(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no repositories configured")
}

func TestDepSync_Run_WithRepositories_Success(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
		Git: config.GitConfig{
			Author: config.GitAuthor{
				Name:  "DepSync Bot",
				Email: "depsync@example.com",
			},
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	expectedResults := map[string][]byte{
		"go.mod": []byte("module github.com/test/repo\nrequire github.com/test/dep v1.0.0\n"),
	}

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(expectedResults, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo": {
			ModulePath:   "github.com/test/repo",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	mismatches := map[string]map[string]depgraph.Mismatch{
		"github.com/test/repo": {
			"github.com/test/dep": {Actual: "v1.0.0", Latest: "v1.1.0"},
		},
	}
	tc.MockChecker.EXPECT().Check(mockGraph).Return(mismatches, nil)

	tc.MockDagger.EXPECT().CloneRepo(gomock.Any(), "https://github.com/test/repo", "main").Return(nil, nil)
	tc.MockDagger.EXPECT().CheckBranchExists(gomock.Any(), dagger.CheckBranchExistsParams{
		Dir:        nil,
		BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		RepoURL:    "https://github.com/test/repo",
	}).Return(false, nil)
	tc.MockDagger.EXPECT().UpdateGoDependency(gomock.Any(), dagger.UpdateGoDependencyParams{
		Dir:           nil,
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
	}).Return(nil, nil)
	tc.MockDagger.EXPECT().CommitAndPush(gomock.Any(), dagger.CommitAndPushParams{
		Dir:           nil,
		BranchName:    "depsync/update-github-com-test-dep-v1.1.0",
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
		AuthorName:    "DepSync Bot",
		AuthorEmail:   "depsync@example.com",
		RepoURL:       "https://github.com/test/repo",
	}).Return("depsync/update-github-com-test-dep-v1.1.0", nil)

	// Mock the CheckPullRequestExists call (returns -1 - no existing PR)
	tc.MockGitHubClient.EXPECT().CheckPullRequestExists(
		gomock.Any(),
		github.CheckPullRequestExistsParams{
			RepoURL:      "https://github.com/test/repo",
			SourceBranch: "depsync/update-github-com-test-dep-v1.1.0",
		},
	).Return(-1, nil)

	// Mock the CreateMergeRequest call
	tc.MockGitHubClient.EXPECT().CreateMergeRequest(
		gomock.Any(),
		github.CreateMergeRequestParams{
			RepoURL:       "https://github.com/test/repo",
			SourceBranch:  "depsync/update-github-com-test-dep-v1.1.0",
			ModulePath:    "github.com/test/dep",
			TargetVersion: "v1.1.0",
		},
	).Return(123, nil)

	// Mock the GetPullRequestChecks call
	tc.MockGitHubClient.EXPECT().GetPullRequestChecks(
		gomock.Any(),
		github.GetPullRequestChecksParams{
			RepoURL:  "https://github.com/test/repo",
			PRNumber: 123,
		},
	).Return(&github.CheckStatus{Status: "running"}, nil)

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.NoError(t, err)
}

func TestDepSync_Run_WithRepositories_ChecksPassAndMerge(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
		Git: config.GitConfig{
			Author: config.GitAuthor{
				Name:  "DepSync Bot",
				Email: "depsync@example.com",
			},
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	expectedResults := map[string][]byte{
		"go.mod": []byte("module github.com/test/repo\nrequire github.com/test/dep v1.0.0\n"),
	}

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(expectedResults, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo": {
			ModulePath:   "github.com/test/repo",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	mismatches := map[string]map[string]depgraph.Mismatch{
		"github.com/test/repo": {
			"github.com/test/dep": {Actual: "v1.0.0", Latest: "v1.1.0"},
		},
	}
	tc.MockChecker.EXPECT().Check(mockGraph).Return(mismatches, nil)

	tc.MockDagger.EXPECT().CloneRepo(gomock.Any(), "https://github.com/test/repo", "main").Return(nil, nil)
	tc.MockDagger.EXPECT().CheckBranchExists(gomock.Any(), dagger.CheckBranchExistsParams{
		Dir:        nil,
		BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		RepoURL:    "https://github.com/test/repo",
	}).Return(false, nil)
	tc.MockDagger.EXPECT().UpdateGoDependency(gomock.Any(), dagger.UpdateGoDependencyParams{
		Dir:           nil,
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
	}).Return(nil, nil)
	tc.MockDagger.EXPECT().CommitAndPush(gomock.Any(), dagger.CommitAndPushParams{
		Dir:           nil,
		BranchName:    "depsync/update-github-com-test-dep-v1.1.0",
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
		AuthorName:    "DepSync Bot",
		AuthorEmail:   "depsync@example.com",
		RepoURL:       "https://github.com/test/repo",
	}).Return("depsync/update-github-com-test-dep-v1.1.0", nil)

	// Mock the CheckPullRequestExists call (returns -1 - no existing PR)
	tc.MockGitHubClient.EXPECT().CheckPullRequestExists(
		gomock.Any(),
		github.CheckPullRequestExistsParams{
			RepoURL:      "https://github.com/test/repo",
			SourceBranch: "depsync/update-github-com-test-dep-v1.1.0",
		},
	).Return(-1, nil)

	// Mock the CreateMergeRequest call
	tc.MockGitHubClient.EXPECT().CreateMergeRequest(
		gomock.Any(),
		github.CreateMergeRequestParams{
			RepoURL:       "https://github.com/test/repo",
			SourceBranch:  "depsync/update-github-com-test-dep-v1.1.0",
			ModulePath:    "github.com/test/dep",
			TargetVersion: "v1.1.0",
		},
	).Return(123, nil)

	// Mock the GetPullRequestChecks call - checks pass
	tc.MockGitHubClient.EXPECT().GetPullRequestChecks(
		gomock.Any(),
		github.GetPullRequestChecksParams{
			RepoURL:  "https://github.com/test/repo",
			PRNumber: 123,
		},
	).Return(&github.CheckStatus{Status: "passed"}, nil)

	// Mock the MergeMergeRequest call
	tc.MockGitHubClient.EXPECT().MergeMergeRequest(
		gomock.Any(),
		github.MergeMergeRequestParams{
			RepoURL:       "https://github.com/test/repo",
			PRNumber:      123,
			ModulePath:    "github.com/test/dep",
			TargetVersion: "v1.1.0",
		},
	).Return(nil)

	// Mock the DeleteBranch call
	tc.MockGitHubClient.EXPECT().DeleteBranch(
		gomock.Any(),
		github.DeleteBranchParams{
			RepoURL:    "https://github.com/test/repo",
			BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		},
	).Return(nil)

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.NoError(t, err)
}

func TestDepSync_Run_WithRepositories_MergeFails(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
		Git: config.GitConfig{
			Author: config.GitAuthor{
				Name:  "DepSync Bot",
				Email: "depsync@example.com",
			},
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	expectedResults := map[string][]byte{
		"go.mod": []byte("module github.com/test/repo\nrequire github.com/test/dep v1.0.0\n"),
	}

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(expectedResults, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo": {
			ModulePath:   "github.com/test/repo",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	mismatches := map[string]map[string]depgraph.Mismatch{
		"github.com/test/repo": {
			"github.com/test/dep": {Actual: "v1.0.0", Latest: "v1.1.0"},
		},
	}
	tc.MockChecker.EXPECT().Check(mockGraph).Return(mismatches, nil)

	tc.MockDagger.EXPECT().CloneRepo(gomock.Any(), "https://github.com/test/repo", "main").Return(nil, nil)
	tc.MockDagger.EXPECT().CheckBranchExists(gomock.Any(), dagger.CheckBranchExistsParams{
		Dir:        nil,
		BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		RepoURL:    "https://github.com/test/repo",
	}).Return(false, nil)
	tc.MockDagger.EXPECT().UpdateGoDependency(gomock.Any(), dagger.UpdateGoDependencyParams{
		Dir:           nil,
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
	}).Return(nil, nil)
	tc.MockDagger.EXPECT().CommitAndPush(gomock.Any(), dagger.CommitAndPushParams{
		Dir:           nil,
		BranchName:    "depsync/update-github-com-test-dep-v1.1.0",
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
		AuthorName:    "DepSync Bot",
		AuthorEmail:   "depsync@example.com",
		RepoURL:       "https://github.com/test/repo",
	}).Return("depsync/update-github-com-test-dep-v1.1.0", nil)

	// Mock the CheckPullRequestExists call (returns -1 - no existing PR)
	tc.MockGitHubClient.EXPECT().CheckPullRequestExists(
		gomock.Any(),
		github.CheckPullRequestExistsParams{
			RepoURL:      "https://github.com/test/repo",
			SourceBranch: "depsync/update-github-com-test-dep-v1.1.0",
		},
	).Return(-1, nil)

	// Mock the CreateMergeRequest call
	tc.MockGitHubClient.EXPECT().CreateMergeRequest(
		gomock.Any(),
		github.CreateMergeRequestParams{
			RepoURL:       "https://github.com/test/repo",
			SourceBranch:  "depsync/update-github-com-test-dep-v1.1.0",
			ModulePath:    "github.com/test/dep",
			TargetVersion: "v1.1.0",
		},
	).Return(123, nil)

	// Mock the GetPullRequestChecks call - checks pass
	tc.MockGitHubClient.EXPECT().GetPullRequestChecks(
		gomock.Any(),
		github.GetPullRequestChecksParams{
			RepoURL:  "https://github.com/test/repo",
			PRNumber: 123,
		},
	).Return(&github.CheckStatus{Status: "passed"}, nil)

	// Mock the MergeMergeRequest call - merge fails
	tc.MockGitHubClient.EXPECT().MergeMergeRequest(
		gomock.Any(),
		github.MergeMergeRequestParams{
			RepoURL:       "https://github.com/test/repo",
			PRNumber:      123,
			ModulePath:    "github.com/test/dep",
			TargetVersion: "v1.1.0",
		},
	).Return(assert.AnError)

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	// The process should continue even if merge fails
	assert.NoError(t, err)
}

func TestDepSync_Run_WithMultipleRepositories_Success(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo1",
			"https://github.com/test/repo2",
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo1", "main", "go.mod").
		Return(map[string][]byte{"go.mod": []byte("module github.com/test/repo1")}, nil)
	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo2", "main", "go.mod").
		Return(map[string][]byte{"go.mod": []byte("module github.com/test/repo2")}, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo1": {
			ModulePath:   "github.com/test/repo1",
			Dependencies: map[string]depgraph.Dependency{},
		},
		"github.com/test/repo2": {
			ModulePath:   "github.com/test/repo2",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	tc.MockChecker.EXPECT().Check(mockGraph).Return(map[string]map[string]depgraph.Mismatch{}, nil)

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.NoError(t, err)
}

func TestDepSync_Run_WithRepositories_FetchError(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(nil, assert.AnError)

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error fetching go.mod")
}

func TestDepSync_Run_WithRepositories_DependencyUpdateError(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	expectedResults := map[string][]byte{
		"go.mod": []byte("module github.com/test/repo\nrequire github.com/test/dep v1.0.0\n"),
	}

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(expectedResults, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo": {
			ModulePath:   "github.com/test/repo",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	mismatches := map[string]map[string]depgraph.Mismatch{
		"github.com/test/repo": {
			"github.com/test/dep": {Actual: "v1.0.0", Latest: "v1.1.0"},
		},
	}
	tc.MockChecker.EXPECT().Check(mockGraph).Return(mismatches, nil)

	tc.MockDagger.EXPECT().CloneRepo(gomock.Any(), "https://github.com/test/repo", "main").Return(nil, nil)
	tc.MockDagger.EXPECT().CheckBranchExists(gomock.Any(), dagger.CheckBranchExistsParams{
		Dir:        nil,
		BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		RepoURL:    "https://github.com/test/repo",
	}).Return(false, nil)
	tc.MockDagger.EXPECT().UpdateGoDependency(gomock.Any(), dagger.UpdateGoDependencyParams{
		Dir:           nil,
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
	}).Return(nil, assert.AnError)

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fix modules")
}

func TestDepSync_Run_WithRepositories_BranchExists(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
		Git: config.GitConfig{
			Author: config.GitAuthor{
				Name:  "DepSync Bot",
				Email: "depsync@example.com",
			},
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	expectedResults := map[string][]byte{
		"go.mod": []byte("module github.com/test/repo\nrequire github.com/test/dep v1.0.0\n"),
	}

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(expectedResults, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo": {
			ModulePath:   "github.com/test/repo",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	mismatches := map[string]map[string]depgraph.Mismatch{
		"github.com/test/repo": {
			"github.com/test/dep": {Actual: "v1.0.0", Latest: "v1.1.0"},
		},
	}
	tc.MockChecker.EXPECT().Check(mockGraph).Return(mismatches, nil)

	// Branch exists, so skip the dependency update but still create MR
	tc.MockDagger.EXPECT().CloneRepo(gomock.Any(), "https://github.com/test/repo", "main").Return(nil, nil)
	tc.MockDagger.EXPECT().CheckBranchExists(gomock.Any(), dagger.CheckBranchExistsParams{
		Dir:        nil,
		BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		RepoURL:    "https://github.com/test/repo",
	}).Return(true, nil)
	// No UpdateGoDependency or CommitAndPush calls expected since branch exists

	// Mock the CheckPullRequestExists call (returns PR number - PR already exists)
	tc.MockGitHubClient.EXPECT().CheckPullRequestExists(
		gomock.Any(),
		github.CheckPullRequestExistsParams{
			RepoURL:      "https://github.com/test/repo",
			SourceBranch: "depsync/update-github-com-test-dep-v1.1.0",
		},
	).Return(123, nil)

	// Mock the GetPullRequestChecks call for existing PR
	tc.MockGitHubClient.EXPECT().GetPullRequestChecks(
		gomock.Any(),
		github.GetPullRequestChecksParams{
			RepoURL:  "https://github.com/test/repo",
			PRNumber: 123,
		},
	).Return(&github.CheckStatus{Status: "running"}, nil)

	// No CreateMergeRequest call expected since PR already exists

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.NoError(t, err)
}

func TestDepSync_Run_WithRepositories_CheckBranchExistsError(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	expectedResults := map[string][]byte{
		"go.mod": []byte("module github.com/test/repo\nrequire github.com/test/dep v1.0.0\n"),
	}

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(expectedResults, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo": {
			ModulePath:   "github.com/test/repo",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	mismatches := map[string]map[string]depgraph.Mismatch{
		"github.com/test/repo": {
			"github.com/test/dep": {Actual: "v1.0.0", Latest: "v1.1.0"},
		},
	}
	tc.MockChecker.EXPECT().Check(mockGraph).Return(mismatches, nil)

	tc.MockDagger.EXPECT().CloneRepo(gomock.Any(), "https://github.com/test/repo", "main").Return(nil, nil)
	tc.MockDagger.EXPECT().CheckBranchExists(gomock.Any(), dagger.CheckBranchExistsParams{
		Dir:        nil,
		BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		RepoURL:    "https://github.com/test/repo",
	}).Return(false, assert.AnError)

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fix modules")
}

func TestDepSync_Run_WithRepositories_CommitAndPushError(t *testing.T) {
	cfg := &config.Config{
		Repositories: []string{
			"https://github.com/test/repo",
		},
		Git: config.GitConfig{
			Author: config.GitAuthor{
				Name:  "DepSync Bot",
				Email: "depsync@example.com",
			},
		},
	}

	tc := newTestDepSync(t, cfg)
	defer tc.MockController.Finish()
	defer tc.DepSync.Close()

	expectedResults := map[string][]byte{
		"go.mod": []byte("module github.com/test/repo\nrequire github.com/test/dep v1.0.0\n"),
	}

	tc.MockFetcher.EXPECT().
		Fetch(gomock.Any(), "https://github.com/test/repo", "main", "go.mod").
		Return(expectedResults, nil)

	mockGraph := map[string]*depgraph.Service{
		"github.com/test/repo": {
			ModulePath:   "github.com/test/repo",
			Dependencies: map[string]depgraph.Dependency{},
		},
	}
	tc.MockGraphBuilder.EXPECT().BuildGraph(gomock.Any()).Return(mockGraph, nil)

	tc.MockVersionDetector.EXPECT().DetectAndSetCurrentVersions(gomock.Any(), gomock.Any(), mockGraph).Return(nil)

	mismatches := map[string]map[string]depgraph.Mismatch{
		"github.com/test/repo": {
			"github.com/test/dep": {Actual: "v1.0.0", Latest: "v1.1.0"},
		},
	}
	tc.MockChecker.EXPECT().Check(mockGraph).Return(mismatches, nil)

	tc.MockDagger.EXPECT().CloneRepo(gomock.Any(), "https://github.com/test/repo", "main").Return(nil, nil)
	tc.MockDagger.EXPECT().CheckBranchExists(gomock.Any(), dagger.CheckBranchExistsParams{
		Dir:        nil,
		BranchName: "depsync/update-github-com-test-dep-v1.1.0",
		RepoURL:    "https://github.com/test/repo",
	}).Return(false, nil)
	tc.MockDagger.EXPECT().UpdateGoDependency(gomock.Any(), dagger.UpdateGoDependencyParams{
		Dir:           nil,
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
	}).Return(nil, nil)
	tc.MockDagger.EXPECT().CommitAndPush(gomock.Any(), dagger.CommitAndPushParams{
		Dir:           nil,
		BranchName:    "depsync/update-github-com-test-dep-v1.1.0",
		ModulePath:    "github.com/test/dep",
		TargetVersion: "v1.1.0",
		AuthorName:    "DepSync Bot",
		AuthorEmail:   "depsync@example.com",
		RepoURL:       "https://github.com/test/repo",
	}).Return("", assert.AnError)

	// No CheckPullRequestExists or CreateMergeRequest calls expected since CommitAndPush failed

	ctx := context.Background()
	err := tc.DepSync.Run(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fix modules")
}
