package automations

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"buddy/internal/tasks"
)

type githubChangedFile struct {
	Name      string `json:"filename"`
	Status    string `json:"status"`
	Patch     string `json:"patch"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

func githubTextPage(result *tasks.GitHubResult, text string, page int) {
	const size = 12000
	runes := []rune(strings.ToValidUTF8(text, "�"))
	start := min((max(1, page)-1)*size, len(runes))
	end := min(start+size, len(runes))
	result.Patch = string(runes[start:end])
	result.More = end < len(runes)
	if result.More {
		result.NextPage = max(1, page) + 1
	}
}

func (s *Service) readDeveloperGitHub(query tasks.GitHubQuery, get func(string, any) error, web string) (tasks.GitHubResult, error) {
	page := max(1, query.Page)
	result := tasks.GitHubResult{Repo: query.Repo, URL: web}
	switch query.Kind {
	case "pull_files", "pull_file":
		endpoint := fmt.Sprintf("/pulls/%d", query.Number)
		var pull githubThread
		if err := get(endpoint, &pull); err != nil {
			return result, err
		}
		if pull.Number != query.Number || !gitCommit.MatchString(pull.Head.SHA) {
			return result, &tasks.InputError{Message: "GitHub returned an invalid pull request."}
		}
		if (query.Ref != "" && query.Ref != pull.Head.SHA) || (query.BaseRef != "" && query.BaseRef != pull.Base.SHA) {
			return result, &tasks.InputError{Message: "The pull request changed. Read it again and use its new head_sha and base_sha for the review."}
		}
		result.HeadSHA, result.URL = pull.Head.SHA, fmt.Sprintf("%s/pull/%d/files", web, query.Number)
		result.BaseSHA = pull.Base.SHA
		result.Heading = fmt.Sprintf("PR #%d changed files", query.Number)
		if query.Kind == "pull_file" {
			if query.Path == "" {
				return result, &tasks.InputError{Message: "Choose an exact file path from pull_files."}
			}
			found := false
			for filePage := 1; filePage <= 30; filePage++ {
				var files []githubChangedFile
				if err := get(fmt.Sprintf("%s/files?per_page=100&page=%d", endpoint, filePage), &files); err != nil {
					return result, err
				}
				if index := slices.IndexFunc(files, func(file githubChangedFile) bool { return file.Name == query.Path }); index >= 0 {
					found = true
					result.Heading = query.Path
					result.Detail = "Diff at " + pull.Head.SHA
					githubTextPage(&result, files[index].Patch, page)
					if files[index].Patch == "" {
						result.Detail += ". GitHub omitted this patch. Read the source at head_sha or inspect it in a checkout."
					}
					break
				}
				if len(files) < 100 {
					break
				}
			}
			if !found {
				return result, &tasks.InputError{Message: "That file is absent from the changed-file list returned by GitHub."}
			}
		} else {
			var files []githubChangedFile
			if err := get(fmt.Sprintf("%s/files?per_page=20&page=%d", endpoint, page), &files); err != nil {
				return result, err
			}
			for _, file := range files {
				result.Items = append(result.Items, tasks.GitHubItem{Title: file.Name,
					Detail: fmt.Sprintf("%s · +%d −%d", file.Status, file.Additions, file.Deletions)})
			}
			result.Detail = fmt.Sprintf("Page %d · %d changed files · use pull_file to read each returned patch", page, pull.ChangedFiles)
			if pull.ChangedFiles > 3000 {
				result.Detail += ". GitHub returns at most 3,000 files; use a checkout for complete coverage."
			}
			result.More = page*20 < pull.ChangedFiles && len(files) == 20 && page < 150
			if result.More {
				result.NextPage = page + 1
			}
		}
		// A force-push during the reads must not combine patches from different heads.
		var current githubThread
		if err := get(endpoint, &current); err != nil {
			return result, err
		}
		if current.Head.SHA != pull.Head.SHA || current.Base.SHA != pull.Base.SHA {
			return tasks.GitHubResult{}, &tasks.InputError{Message: "The pull request changed during the read. Restart the review using its current head_sha."}
		}
	case "file":
		if query.Path == "" {
			return result, &tasks.InputError{Message: "Provide the repository file path to read."}
		}
		var file struct{ Type, Encoding, Content string }
		path := (&url.URL{Path: query.Path}).EscapedPath()
		parameters := url.Values{}
		if query.Ref != "" {
			parameters.Set("ref", query.Ref)
		}
		if err := get("/contents/"+path+"?"+parameters.Encode(), &file); err != nil {
			return result, err
		}
		if file.Type != "file" || file.Encoding != "base64" {
			return result, &tasks.InputError{Message: "GitHub did not return readable file content. Inspect this path in a checkout."}
		}
		content, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return result, &tasks.InputError{Message: "GitHub returned invalid file content."}
		}
		ref := query.Ref
		if ref == "" {
			ref = "HEAD"
		}
		result.Heading, result.Detail = query.Path, "Ref "+ref
		result.URL = web + "/blob/" + url.PathEscape(ref) + "/" + path
		if gitCommit.MatchString(query.Ref) {
			result.HeadSHA = query.Ref
		}
		githubTextPage(&result, string(content), page)
	case "comments", "review_comments":
		var comments []struct {
			Body string
			URL  string `json:"html_url"`
			User struct{ Login string }
			Path string
			Line int
		}
		endpoint := fmt.Sprintf("/issues/%d/comments?per_page=20&page=%d", query.Number, page)
		if query.Kind == "review_comments" {
			endpoint = fmt.Sprintf("/pulls/%d/comments?per_page=20&page=%d", query.Number, page)
		}
		if err := get(endpoint, &comments); err != nil {
			return result, err
		}
		result.Heading, result.URL = fmt.Sprintf("Discussion #%d", query.Number), fmt.Sprintf("%s/issues/%d", web, query.Number)
		for _, comment := range comments {
			if comment.Path != "" {
				comment.Body = fmt.Sprintf("%s:%d\n%s", comment.Path, comment.Line, comment.Body)
			}
			result.Items = append(result.Items, tasks.GitHubItem{Title: "@" + cleanHeading(comment.User.Login, 80),
				Detail: clipGitHubText(comment.Body, 1800), URL: comment.URL})
		}
		result.More = len(comments) == 20 && page < 150
		if result.More {
			result.NextPage = page + 1
		}
	case "jobs":
		if query.RunID < 1 {
			return result, &tasks.InputError{Message: "Provide the Actions run ID from its run URL."}
		}
		var jobs struct {
			Total int `json:"total_count"`
			Jobs  []struct {
				Name, Status, Conclusion string
				URL                      string `json:"html_url"`
				Steps                    []struct{ Name, Status, Conclusion string }
			}
		}
		if err := get(fmt.Sprintf("/actions/runs/%d/jobs?per_page=20&page=%d", query.RunID, page), &jobs); err != nil {
			return result, err
		}
		result.Heading, result.URL = "Workflow jobs and steps", fmt.Sprintf("%s/actions/runs/%d", web, query.RunID)
		for _, job := range jobs.Jobs {
			detail := job.Status + " " + job.Conclusion
			for _, step := range job.Steps {
				detail += "\n" + cleanHeading(step.Name, 180) + " · " + step.Status + " " + step.Conclusion
			}
			result.Items = append(result.Items, tasks.GitHubItem{Title: cleanHeading(job.Name, 220), Detail: detail, URL: job.URL})
		}
		result.More = page*20 < jobs.Total && len(jobs.Jobs) == 20 && page < 150
		if result.More {
			result.NextPage = page + 1
		}
	}
	return result, nil
}
