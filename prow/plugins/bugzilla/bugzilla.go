/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package bugzilla ensures that pull requests reference a Bugzilla bug in their title
package bugzilla

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	githubql "github.com/shurcooL/githubv4"
	"github.com/sirupsen/logrus"

	"k8s.io/test-infra/prow/bugzilla"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/labels"
	"k8s.io/test-infra/prow/pluginhelp"
	"k8s.io/test-infra/prow/plugins"
)

var (
	titleMatch          = regexp.MustCompile(`(?i)^.*?Bug ([0-9]+):`)
	refreshCommandMatch = regexp.MustCompile(`(?mi)^/bugzilla refresh\s*$`)
	qaCommandMatch      = regexp.MustCompile(`(?mi)^/bugzilla assign-qa\s*$`)
)

const (
	PluginName = "bugzilla"
	bugLink    = `[Bugzilla bug %d](%s/show_bug.cgi?id=%d)`
)

func init() {
	plugins.RegisterGenericCommentHandler(PluginName, handleGenericComment, helpProvider)
	plugins.RegisterPullRequestHandler(PluginName, handlePullRequest, helpProvider)
}

func helpProvider(config *plugins.Configuration, enabledRepos []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
	configInfo := make(map[string]string)
	for _, repo := range enabledRepos {
		opts := config.Bugzilla.OptionsForRepo(repo.Org, repo.Repo)
		if len(opts) == 0 {
			continue
		}
		// we need to make sure the order of this help is consistent for page reloads and testing
		var branches []string
		for branch := range opts {
			branches = append(branches, branch)
		}
		sort.Strings(branches)
		var configInfoStrings []string
		configInfoStrings = append(configInfoStrings, "The plugin has the following configuration:<ul>")
		for _, branch := range branches {
			var message string
			if branch == plugins.BugzillaOptionsWildcard {
				message = "by default, "
			} else {
				message = fmt.Sprintf("on the %q branch, ", branch)
			}
			message += "valid bugs must "
			var conditions []string
			if opts[branch].IsOpen != nil {
				if *opts[branch].IsOpen {
					conditions = append(conditions, "be open")
				} else {
					conditions = append(conditions, "be closed")
				}
			}
			if opts[branch].TargetRelease != nil {
				conditions = append(conditions, fmt.Sprintf("target the %q release", *opts[branch].TargetRelease))
			}
			if opts[branch].ValidStates != nil && len(*opts[branch].ValidStates) > 0 {
				pretty := strings.Join(prettyStates(*opts[branch].ValidStates), ", ")
				conditions = append(conditions, fmt.Sprintf("be in one of the following states: %s", pretty))
			}
			if opts[branch].DependentBugStates != nil || opts[branch].DependentBugTargetRelease != nil {
				conditions = append(conditions, "depend on at least one other bug")
			}
			if opts[branch].DependentBugStates != nil {
				pretty := strings.Join(prettyStates(*opts[branch].DependentBugStates), ", ")
				conditions = append(conditions, fmt.Sprintf("have all dependent bugs in one of the following states: %s", pretty))
			}
			if opts[branch].DependentBugTargetRelease != nil {
				conditions = append(conditions, fmt.Sprintf("have all dependent bugs target the %q release", *opts[branch].DependentBugTargetRelease))
			}
			switch len(conditions) {
			case 0:
				message += "exist"
			case 1:
				message += conditions[0]
			case 2:
				message += fmt.Sprintf("%s and %s", conditions[0], conditions[1])
			default:
				conditions[len(conditions)-1] = fmt.Sprintf("and %s", conditions[len(conditions)-1])
				message += strings.Join(conditions, ", ")
			}
			var updates []string
			if opts[branch].StateAfterValidation != nil {
				updates = append(updates, fmt.Sprintf("moved to the %s state", opts[branch].StateAfterValidation))
			}
			if opts[branch].AddExternalLink != nil && *opts[branch].AddExternalLink {
				updates = append(updates, "updated to refer to the pull request using the external bug tracker")
			}
			if opts[branch].StateAfterMerge != nil {
				updates = append(updates, fmt.Sprintf("moved to the %s state when all linked pull requests are merged", opts[branch].StateAfterMerge))
			}

			if len(updates) > 0 {
				message += ". After being linked to a pull request, bugs will be "
			}
			switch len(updates) {
			case 0:
			case 1:
				message += updates[0]
			case 2:
				message += fmt.Sprintf("%s and %s", updates[0], updates[1])
			default:
				updates[len(updates)-1] = fmt.Sprintf("and %s", updates[len(updates)-1])
				message += strings.Join(updates, ", ")
			}
			configInfoStrings = append(configInfoStrings, "<li>"+message+".</li>")
		}
		configInfoStrings = append(configInfoStrings, "</ul>")

		configInfo[repo.String()] = strings.Join(configInfoStrings, "\n")
	}
	pluginHelp := &pluginhelp.PluginHelp{
		Description: "The bugzilla plugin ensures that pull requests reference a valid Bugzilla bug in their title.",
		Config:      configInfo,
	}
	pluginHelp.AddCommand(pluginhelp.Command{
		Usage:       "/bugzilla refresh",
		Description: "Check Bugzilla for a valid bug referenced in the PR title",
		Featured:    false,
		WhoCanUse:   "Anyone",
		Examples:    []string{"/bugzilla refresh"},
	})
	pluginHelp.AddCommand(pluginhelp.Command{
		Usage:       "/bugzilla assign-qa",
		Description: "Assign PR to QA contact specified in Bugzilla",
		Featured:    false,
		WhoCanUse:   "Anyone",
		Examples:    []string{"/bugzilla assign-qa"},
	})
	return pluginHelp, nil
}

type githubClient interface {
	GetPullRequest(org, repo string, number int) (*github.PullRequest, error)
	CreateComment(owner, repo string, number int, comment string) error
	GetIssueLabels(org, repo string, number int) ([]github.Label, error)
	AddLabel(owner, repo string, number int, label string) error
	RemoveLabel(owner, repo string, number int, label string) error
	Query(ctx context.Context, q interface{}, vars map[string]interface{}) error
}

func handleGenericComment(pc plugins.Agent, e github.GenericCommentEvent) error {
	event, err := digestComment(pc.GitHubClient, pc.Logger, e)
	if err != nil {
		return err
	}
	if event != nil {
		options := pc.PluginConfig.Bugzilla.OptionsForBranch(event.org, event.repo, event.baseRef)
		return handle(*event, pc.GitHubClient, pc.BugzillaClient, options, pc.Logger)
	}
	return nil
}

func handlePullRequest(pc plugins.Agent, pre github.PullRequestEvent) error {
	options := pc.PluginConfig.Bugzilla.OptionsForBranch(pre.PullRequest.Base.Repo.Owner.Login, pre.PullRequest.Base.Repo.Name, pre.PullRequest.Base.Ref)
	event, err := digestPR(pc.Logger, pre, options.ValidateByDefault)
	if err != nil {
		return err
	}
	if event != nil {
		return handle(*event, pc.GitHubClient, pc.BugzillaClient, options, pc.Logger)
	}
	return nil
}

// digestPR determines if any action is necessary and creates the objects for handle() if it is
func digestPR(log *logrus.Entry, pre github.PullRequestEvent, validateByDefault *bool) (*event, error) {
	// These are the only actions indicating the PR title may have changed or that the PR merged
	if pre.Action != github.PullRequestActionOpened &&
		pre.Action != github.PullRequestActionReopened &&
		pre.Action != github.PullRequestActionEdited &&
		!(pre.Action == github.PullRequestActionClosed && pre.PullRequest.Merged) {
		return nil, nil
	}

	var (
		org     = pre.PullRequest.Base.Repo.Owner.Login
		repo    = pre.PullRequest.Base.Repo.Name
		baseRef = pre.PullRequest.Base.Ref
		number  = pre.PullRequest.Number
		title   = pre.PullRequest.Title
	)

	// Make sure the PR title is referencing a bug
	e := &event{org: org, repo: repo, baseRef: baseRef, number: number, merged: pre.PullRequest.Merged, state: pre.PullRequest.State, body: title, htmlUrl: pre.PullRequest.HTMLURL, login: pre.PullRequest.User.Login}
	mat := titleMatch.FindStringSubmatch(title)
	if mat == nil {
		// in the case that the title used to reference a bug and no longer does we
		// want to handle this to remove labels
		e.missing = true
	} else {
		id, err := strconv.Atoi(mat[1])
		if err != nil {
			// should be impossible based on the regex
			log.WithError(err).Debug("Failed to parse bug ID as int - is the regex correct?")
			return nil, err
		}
		e.bugId = id
	}

	// when exiting early from errors trying to find out if the PR previously referenced a bug,
	// we want to handle the event only if a bug is currently referenced or we are validating by
	// default
	var intermediate *event
	if !e.missing || (validateByDefault != nil && *validateByDefault) {
		intermediate = e
	}

	// Check if the previous version of the title referenced a bug.
	var changes struct {
		Title struct {
			From string `json:"from"`
		} `json:"title"`
	}
	if err := json.Unmarshal(pre.Changes, &changes); err != nil {
		// we're detecting this best-effort so we can handle it anyway
		return intermediate, nil
	}
	prevMat := titleMatch.FindStringSubmatch(changes.Title.From)
	if prevMat == nil {
		// title did not previously reference a bug
		return intermediate, nil
	}
	prevId, err := strconv.Atoi(prevMat[1])
	if err != nil {
		// should be impossible based on the regex, ignore err as this is best-effort
		log.WithError(err).Debug("Failed to parse bug ID as int - is the regex correct?")
		return intermediate, nil
	}

	// if the referenced bug has not changed in the update, ignore it
	if prevId == e.bugId {
		logrus.Debugf("Referenced Bugzilla ID (%d) has not changed, not handling event.", e.bugId)
		return nil, nil
	}

	// we know the PR previously referenced a bug, so whether
	// it currently does or does not reference a bug, we should
	// handle the event
	return e, nil
}

// digestComment determines if any action is necessary and creates the objects for handle() if it is
func digestComment(gc githubClient, log *logrus.Entry, gce github.GenericCommentEvent) (*event, error) {
	// Only consider new comments.
	if gce.Action != github.GenericCommentActionCreated {
		return nil, nil
	}
	// Make sure they are requesting a valid command
	var assign bool
	switch {
	case refreshCommandMatch.MatchString(gce.Body):
		assign = false
	case qaCommandMatch.MatchString(gce.Body):
		assign = true
	default:
		return nil, nil
	}
	var (
		org    = gce.Repo.Owner.Login
		repo   = gce.Repo.Name
		number = gce.Number
	)

	// We don't support linking issues to Bugs
	if !gce.IsPR {
		log.Debug("Bugzilla command requested on an issue, ignoring")
		return nil, gc.CreateComment(org, repo, number, plugins.FormatResponseRaw(gce.Body, gce.HTMLURL, gce.User.Login, `Bugzilla bug referencing is only supported for Pull Requests, not issues.`))
	}

	// Make sure the PR title is referencing a bug
	pr, err := gc.GetPullRequest(org, repo, number)
	if err != nil {
		return nil, err
	}

	e := &event{org: org, repo: repo, baseRef: pr.Base.Ref, number: number, merged: pr.Merged, state: pr.State, body: gce.Body, htmlUrl: gce.HTMLURL, login: gce.User.Login, assign: assign}
	mat := titleMatch.FindStringSubmatch(pr.Title)
	if mat == nil {
		e.missing = true
		return e, nil
	}
	id, err := strconv.Atoi(mat[1])
	if err != nil {
		// should be impossible based on the regex
		log.WithError(err).Debug("Failed to parse bug ID as int - is the regex correct?")
		return nil, err
	}
	e.bugId = id

	return e, nil
}

type event struct {
	org, repo, baseRef   string
	number, bugId        int
	missing, merged      bool
	state                string
	body, htmlUrl, login string
	assign               bool
}

func (e *event) comment(gc githubClient) func(body string) error {
	return func(body string) error {
		return gc.CreateComment(e.org, e.repo, e.number, plugins.FormatResponseRaw(e.body, e.htmlUrl, e.login, body))
	}
}

type queryUser struct {
	Login githubql.String
}

type queryNode struct {
	User queryUser `graphql:"... on User"`
}

type queryEdge struct {
	Node queryNode
}

type querySearch struct {
	Edges []queryEdge
}

/* emailToLoginQuery is a graphql query struct that should result in this graphql query:
   {
     search(type: USER, query: "email", first: 5) {
       edges {
         node {
           ... on User {
             login
           }
         }
       }
     }
   }
*/
type emailToLoginQuery struct {
	Search querySearch `graphql:"search(type:USER query:$email first:5)"`
}

// processQueryResult generates a response based on a populated emailToLoginQuery
func processQuery(query *emailToLoginQuery, email string, log *logrus.Entry) string {
	switch len(query.Search.Edges) {
	case 0:
		return fmt.Sprintf("No GitHub users were found matching the public email listed for the QA contact in Bugzilla (%s), skipping assignment.", email)
	case 1:
		return fmt.Sprintf("Assigning the QA contact for review:\n/assign @%s", query.Search.Edges[0].Node.User.Login)
	default:
		response := fmt.Sprintf("Multiple GitHub users were found matching the public email listed for the QA contact in Bugzilla (%s), skipping assignment. List of users with matching email:", email)
		for _, edge := range query.Search.Edges {
			response += fmt.Sprintf("\n\t- %s", edge.Node.User.Login)
		}
		return response
	}
}

func handle(e event, gc githubClient, bc bugzilla.Client, options plugins.BugzillaBranchOptions, log *logrus.Entry) error {
	comment := e.comment(gc)
	// merges follow a different pattern from the normal validation
	if e.merged {
		return handleMerge(e, gc, bc, options, log)
	}

	var needsValidLabel, needsInvalidLabel bool
	var response string
	if e.missing {
		log.WithField("bugMissing", true)
		log.Debug("No bug referenced.")
		needsValidLabel, needsInvalidLabel = false, false
		response = `No Bugzilla bug is referenced in the title of this pull request.
To reference a bug, add 'Bug XXX:' to the title of this pull request and request another bug refresh with <code>/bugzilla refresh</code>.`
	} else {
		log = log.WithField("bugId", e.bugId)

		bug, err := getBug(bc, e.bugId, log, comment)
		if err != nil || bug == nil {
			return err
		}

		var dependents []bugzilla.Bug
		if options.DependentBugStates != nil || options.DependentBugTargetRelease != nil {
			for _, id := range bug.DependsOn {
				dependent, err := bc.GetBug(id)
				if err != nil {
					return comment(formatError(fmt.Sprintf("searching for dependent bug %d", id), bc.Endpoint(), e.bugId, err))
				}
				dependents = append(dependents, *dependent)
			}
		}

		valid, validationsRun, why := validateBug(*bug, dependents, options, bc.Endpoint())
		needsValidLabel, needsInvalidLabel = valid, !valid
		if valid {
			log.Debug("Valid bug found.")
			response = fmt.Sprintf(`This pull request references `+bugLink+`, which is valid.`, e.bugId, bc.Endpoint(), e.bugId)
			// if configured, move the bug to the new state
			if update := options.StateAfterValidation.AsBugUpdate(bug); update != nil {
				if err := bc.UpdateBug(e.bugId, *update); err != nil {
					log.WithError(err).Warn("Unexpected error updating Bugzilla bug.")
					return comment(formatError(fmt.Sprintf("updating to the %s state", options.StateAfterValidation), bc.Endpoint(), e.bugId, err))
				}
				response += fmt.Sprintf(" The bug has been moved to the %s state.", options.StateAfterValidation)
			}
			if options.AddExternalLink != nil && *options.AddExternalLink {
				changed, err := bc.AddPullRequestAsExternalBug(e.bugId, e.org, e.repo, e.number)
				if err != nil {
					log.WithError(err).Warn("Unexpected error adding external tracker bug to Bugzilla bug.")
					return comment(formatError("adding this pull request to the external tracker bugs", bc.Endpoint(), e.bugId, err))
				}
				if changed {
					response += " The bug has been updated to refer to the pull request using the external bug tracker."
				}
			}

			response += "\n\n<details>"
			if len(validationsRun) == 0 {
				response += "<summary>No validations were run on this bug</summary>"
			} else {
				response += fmt.Sprintf("<summary>%d validation(s) were run on this bug</summary>\n", len(validationsRun))
			}
			for _, validation := range validationsRun {
				response += fmt.Sprint("\n* ", validation)
			}
			response += "</details>"

			// if bug is valid and qa command was used, identify qa contact via email
			if e.assign {
				if bug.QAContactDetail == nil {
					response += fmt.Sprintf(bugLink+" does not have a QA contact, skipping assignment", e.bugId, bc.Endpoint(), e.bugId)
				} else if bug.QAContactDetail.Email == "" {
					response += fmt.Sprintf("QA contact for "+bugLink+" does not have a listed email, skipping assignment", e.bugId, bc.Endpoint(), e.bugId)
				} else {
					query := &emailToLoginQuery{}
					email := bug.QAContactDetail.Email
					queryVars := map[string]interface{}{
						"email": githubql.String(email),
					}
					err := gc.Query(context.Background(), query, queryVars)
					if err != nil {
						log.WithError(err).Error("Failed to run graphql github query")
						return comment(formatError(fmt.Sprintf("querying GitHub for users with public email (%s)", email), bc.Endpoint(), e.bugId, err))
					}
					response += fmt.Sprint("\n\n", processQuery(query, email, log))
				}
			}
		} else {
			log.Debug("Invalid bug found.")
			var formattedReasons string
			for _, reason := range why {
				formattedReasons += fmt.Sprintf(" - %s\n", reason)
			}
			response = fmt.Sprintf(`This pull request references `+bugLink+`, which is invalid:
%s
Comment <code>/bugzilla refresh</code> to re-evaluate validity if changes to the Bugzilla bug are made, or edit the title of this pull request to link to a different bug.`, e.bugId, bc.Endpoint(), e.bugId, formattedReasons)
		}
	}

	// ensure label state is correct. Do not propagate errors
	// as it is more important to report to the user than to
	// fail early on a label check.
	currentLabels, err := gc.GetIssueLabels(e.org, e.repo, e.number)
	if err != nil {
		log.WithError(err).Warn("Could not list labels on PR")
	}
	var hasValidLabel, hasInvalidLabel bool
	for _, l := range currentLabels {
		if l.Name == labels.ValidBug {
			hasValidLabel = true
		}
		if l.Name == labels.InvalidBug {
			hasInvalidLabel = true
		}
	}

	if needsValidLabel && !hasValidLabel {
		if err := gc.AddLabel(e.org, e.repo, e.number, labels.ValidBug); err != nil {
			log.WithError(err).Error("Failed to add valid bug label.")
		}
	} else if !needsValidLabel && hasValidLabel {
		if err := gc.RemoveLabel(e.org, e.repo, e.number, labels.ValidBug); err != nil {
			log.WithError(err).Error("Failed to remove valid bug label.")
		}
	}

	if needsInvalidLabel && !hasInvalidLabel {
		if err := gc.AddLabel(e.org, e.repo, e.number, labels.InvalidBug); err != nil {
			log.WithError(err).Error("Failed to add invalid bug label.")
		}
	} else if !needsInvalidLabel && hasInvalidLabel {
		if err := gc.RemoveLabel(e.org, e.repo, e.number, labels.InvalidBug); err != nil {
			log.WithError(err).Error("Failed to remove invalid bug label.")
		}
	}

	return comment(response)
}

func bugMatchesStates(bug *bugzilla.Bug, states []plugins.BugzillaBugState) bool {
	for _, state := range states {
		if (&state).Matches(bug) {
			return true
		}
	}
	return false
}

func prettyStates(statuses []plugins.BugzillaBugState) []string {
	pretty := make([]string, 0, len(statuses))
	for _, status := range statuses {
		pretty = append(pretty, bugzilla.PrettyStatus(status.Status, status.Resolution))
	}
	return pretty
}

// validateBug determines if the bug matches the options and returns a description of why not
func validateBug(bug bugzilla.Bug, dependents []bugzilla.Bug, options plugins.BugzillaBranchOptions, endpoint string) (bool, []string, []string) {
	valid := true
	var errors []string
	var validations []string
	if options.IsOpen != nil && *options.IsOpen != bug.IsOpen {
		valid = false
		not := ""
		was := "isn't"
		if !*options.IsOpen {
			not = "not "
			was = "is"
		}
		errors = append(errors, fmt.Sprintf("expected the bug to %sbe open, but it %s", not, was))
	} else if options.IsOpen != nil {
		expected := "open"
		if !*options.IsOpen {
			expected = "not open"
		}
		was := "isn't"
		if bug.IsOpen {
			was = "is"
		}
		validations = append(validations, fmt.Sprintf("bug %s open, matching expected state (%s)", was, expected))
	}

	if options.TargetRelease != nil {
		if len(bug.TargetRelease) == 0 {
			valid = false
			errors = append(errors, fmt.Sprintf("expected the bug to target the %q release, but no target release was set", *options.TargetRelease))
		} else if *options.TargetRelease != bug.TargetRelease[0] {
			// the BugZilla web UI shows one option for target release, but returns the
			// field as a list in the REST API. We only care for the first item and it's
			// not even clear if the list can have more than one item in the response
			valid = false
			errors = append(errors, fmt.Sprintf("expected the bug to target the %q release, but it targets %q instead", *options.TargetRelease, bug.TargetRelease[0]))
		} else {
			validations = append(validations, fmt.Sprintf("bug target release (%s) matches configured target release for branch (%s)", bug.TargetRelease[0], *options.TargetRelease))
		}
	}

	if options.ValidStates != nil {
		var allowed []plugins.BugzillaBugState
		allowed = append(allowed, *options.ValidStates...)
		if options.StateAfterValidation != nil {
			allowed = append(allowed, *options.StateAfterValidation)
		}
		if !bugMatchesStates(&bug, allowed) {
			valid = false
			errors = append(errors, fmt.Sprintf("expected the bug to be in one of the following states: %s, but it is %s instead", strings.Join(prettyStates(allowed), ", "), bugzilla.PrettyStatus(bug.Status, bug.Resolution)))
		} else {
			validations = append(validations, fmt.Sprintf("bug is in the state %s, which is one of the valid states (%s)", bugzilla.PrettyStatus(bug.Status, bug.Resolution), strings.Join(prettyStates(allowed), ", ")))
		}
	}

	if options.DependentBugStates != nil {
		for _, bug := range dependents {
			if !bugMatchesStates(&bug, *options.DependentBugStates) {
				valid = false
				expected := strings.Join(prettyStates(*options.DependentBugStates), ", ")
				actual := bugzilla.PrettyStatus(bug.Status, bug.Resolution)
				errors = append(errors, fmt.Sprintf("expected dependent "+bugLink+" to be in one of the following states: %s, but it is %s instead", bug.ID, endpoint, bug.ID, expected, actual))
			} else {
				validations = append(validations, fmt.Sprintf("dependent bug "+bugLink+" is in the state %s, which is one of the valid states (%s)", bug.ID, endpoint, bug.ID, bugzilla.PrettyStatus(bug.Status, bug.Resolution), strings.Join(prettyStates(*options.DependentBugStates), ", ")))
			}
		}
	}

	if options.DependentBugTargetRelease != nil {
		for _, bug := range dependents {
			if len(bug.TargetRelease) == 0 {
				valid = false
				errors = append(errors, fmt.Sprintf("expected dependent "+bugLink+" to target the %q release, but no target release was set", bug.ID, endpoint, bug.ID, *options.DependentBugTargetRelease))
			} else if *options.DependentBugTargetRelease != bug.TargetRelease[0] {
				// the BugZilla web UI shows one option for target release, but returns the
				// field as a list in the REST API. We only care for the first item and it's
				// not even clear if the list can have more than one item in the response
				valid = false
				errors = append(errors, fmt.Sprintf("expected dependent "+bugLink+" to target the %q release, but it targets %q instead", bug.ID, endpoint, bug.ID, *options.DependentBugTargetRelease, bug.TargetRelease[0]))
			} else {
				validations = append(validations, fmt.Sprintf("dependent "+bugLink+" targets the %q release, matching the expected (%s) release", bug.ID, endpoint, bug.ID, bug.TargetRelease[0], *options.DependentBugTargetRelease))
			}
		}
	}

	if len(dependents) == 0 {
		switch {
		case options.DependentBugStates != nil && options.DependentBugTargetRelease != nil:
			valid = false
			expected := strings.Join(prettyStates(*options.DependentBugStates), ", ")
			errors = append(errors, fmt.Sprintf("expected "+bugLink+" to depend on a bug targeting the %q release and in one of the following states: %s, but no dependents were found", bug.ID, endpoint, bug.ID, *options.DependentBugTargetRelease, expected))
		case options.DependentBugStates != nil:
			valid = false
			expected := strings.Join(prettyStates(*options.DependentBugStates), ", ")
			errors = append(errors, fmt.Sprintf("expected "+bugLink+" to depend on a bug in one of the following states: %s, but no dependents were found", bug.ID, endpoint, bug.ID, expected))
		case options.DependentBugTargetRelease != nil:
			valid = false
			errors = append(errors, fmt.Sprintf("expected "+bugLink+" to depend on a bug targeting the %q release, but no dependents were found", bug.ID, endpoint, bug.ID, *options.DependentBugTargetRelease))
		default:
		}
	} else {
		validations = append(validations, "bug has dependents")
	}

	return valid, validations, errors
}

func handleMerge(e event, gc githubClient, bc bugzilla.Client, options plugins.BugzillaBranchOptions, log *logrus.Entry) error {
	comment := e.comment(gc)

	if options.StateAfterMerge == nil {
		return nil
	}
	if e.missing {
		return nil
	}
	if options.ValidStates != nil || options.StateAfterValidation != nil {
		// we should only migrate if we can be fairly certain that the bug
		// is not in a state that required human intervention to get to.
		// For instance, if a bug is closed after a PR merges it should not
		// be possible for /bugzilla refresh to move it back to the post-merge
		// state.
		bug, err := getBug(bc, e.bugId, log, comment)
		if err != nil || bug == nil {
			return err
		}
		var allowed []plugins.BugzillaBugState
		if options.ValidStates != nil {
			allowed = append(allowed, *options.ValidStates...)
		}

		if options.StateAfterValidation != nil {
			allowed = append(allowed, *options.StateAfterValidation)
		}
		if !bugMatchesStates(bug, allowed) {
			return comment(fmt.Sprintf(bugLink+" is in an unrecognized state (%s) and will not be moved to the %s state.", e.bugId, bc.Endpoint(), e.bugId, bugzilla.PrettyStatus(bug.Status, bug.Resolution), options.StateAfterMerge))
		}
	}

	prs, err := bc.GetExternalBugPRsOnBug(e.bugId)
	if err != nil {
		log.WithError(err).Warn("Unexpected error listing external tracker bugs for Bugzilla bug.")
		return comment(formatError("searching for external tracker bugs", bc.Endpoint(), e.bugId, err))
	}
	shouldMigrate := true
	var mergedPRs []bugzilla.ExternalBug
	unmergedPrStates := map[bugzilla.ExternalBug]string{}
	for _, item := range prs {
		var merged bool
		var state string
		if e.org == item.Org && e.repo == item.Repo && e.number == item.Num {
			merged = e.merged
			state = e.state
		} else {
			pr, err := gc.GetPullRequest(item.Org, item.Repo, item.Num)
			if err != nil {
				log.WithError(err).Warn("Unexpected error checking merge state of related pull request.")
				return comment(formatError(fmt.Sprintf("checking the state of a related pull request at https://github.com/%s/%s/pull/%d", item.Org, item.Repo, item.Num), bc.Endpoint(), e.bugId, err))
			}
			merged = pr.Merged
			state = pr.State
		}
		if merged {
			mergedPRs = append(mergedPRs, item)
		} else {
			unmergedPrStates[item] = state
		}
		// only update Bugzilla bug status if all PRs have merged
		shouldMigrate = shouldMigrate && merged
		if !shouldMigrate {
			// we could give more complete feedback to the user by checking all PRs
			// but we save tokens by exiting when we find an unmerged one, so we
			// prefer to do that
			break
		}
	}

	link := func(bug bugzilla.ExternalBug) string {
		return fmt.Sprintf("[%s/%s#%d](https://github.com/%s/%s/pull/%d)", bug.Org, bug.Repo, bug.Num, bug.Org, bug.Repo, bug.Num)
	}

	mergedMessage := func(statement string) string {
		var links []string
		for _, bug := range mergedPRs {
			links = append(links, link(bug))
		}
		return fmt.Sprintf(`%s pull requests linked via external trackers have merged: %s.`, statement, strings.Join(links, ", "))
	}

	var statements []string
	for bug, state := range unmergedPrStates {
		statements = append(statements, fmt.Sprintf("\n * %s is %s", link(bug), state))
	}
	unmergedMessage := fmt.Sprintf(`The following pull requests linked via external trackers have not merged:%s`, strings.Join(statements, "\n"))

	outcomeMessage := func(action string) string {
		return fmt.Sprintf(bugLink+" has %sbeen moved to the %s state.", e.bugId, bc.Endpoint(), e.bugId, action, options.StateAfterMerge)
	}

	update := options.StateAfterMerge.AsBugUpdate(nil)
	if update == nil {
		// should never happen
		return nil
	}

	if shouldMigrate {
		if err := bc.UpdateBug(e.bugId, *update); err != nil {
			log.WithError(err).Warn("Unexpected error updating Bugzilla bug.")
			return comment(formatError(fmt.Sprintf("updating to the %s state", options.StateAfterMerge), bc.Endpoint(), e.bugId, err))
		}
		return comment(fmt.Sprintf("%s %s", mergedMessage("All"), outcomeMessage("")))
	}
	return comment(fmt.Sprintf("%s %s\n%s", mergedMessage("Some"), unmergedMessage, outcomeMessage("")))
}

func getBug(bc bugzilla.Client, bugId int, log *logrus.Entry, comment func(string) error) (*bugzilla.Bug, error) {
	bug, err := bc.GetBug(bugId)
	if err != nil && !bugzilla.IsNotFound(err) {
		log.WithError(err).Warn("Unexpected error searching for Bugzilla bug.")
		return nil, comment(formatError("searching", bc.Endpoint(), bugId, err))
	}
	if bugzilla.IsNotFound(err) || bug == nil {
		log.Debug("No bug found.")
		return nil, comment(fmt.Sprintf(`No Bugzilla bug with ID %d exists in the tracker at %s.
Once a valid bug is referenced in the title of this pull request, request a bug refresh with <code>/bugzilla refresh</code>.`,
			bugId, bc.Endpoint()))
	}
	return bug, nil
}

func formatError(action, endpoint string, bugId int, err error) string {
	return fmt.Sprintf(`An error was encountered %s for bug %d on the Bugzilla server at %s:
> %v
Please contact an administrator to resolve this issue, then request a bug refresh with <code>/bugzilla refresh</code>.`,
		action, bugId, endpoint, err)
}
