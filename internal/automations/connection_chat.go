package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"buddy/internal/tasks"
)

const connectionPreviewRows = 6

var providerItemID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func (s *Service) ChatConnections(ctx context.Context) ([]tasks.ProviderConnection, error) {
	connections, err := s.store.ProviderConnections(ctx)
	if err != nil {
		return nil, err
	}
	var active []tasks.ProviderConnection
	for _, c := range connections {
		if c.Connected {
			active = append(active, c)
		}
	}
	return active, nil
}

func (s *Service) PrepareWatchStart(ctx context.Context, watch *tasks.Watch) error {
	if watch.Source != tasks.GmailLabel || watch.ConnectionID == "" {
		return nil
	}
	c, err := s.activeConnection(ctx, watch.ConnectionID)
	if err != nil {
		return err
	}
	if c.Source != watch.Source {
		return &tasks.InputError{Message: "Choose a Gmail account for this watch."}
	}
	token, problem := s.gmailToken(ctx, c)
	if problem != "" {
		return &tasks.InputError{Message: problem}
	}
	var profile struct{ HistoryID string }
	if err := s.getJSON(ctx, gmailAPI+"/profile", token, &profile); err != nil {
		return &tasks.InputError{Message: "Couldn’t read the Gmail watch starting point."}
	}
	if id, err := strconv.ParseUint(profile.HistoryID, 10, 64); err != nil || id == 0 {
		return &tasks.InputError{Message: "Gmail didn’t return a valid watch starting point."}
	}
	watch.GmailHistory = profile.HistoryID
	return nil
}

func (s *Service) ResolveWatchScope(ctx context.Context, w tasks.Watch) (string, string, error) {
	scope := strings.TrimSpace(w.Scope)
	switch w.Source {
	case tasks.GmailLabel:
		labels, err := s.ConnectionScopes(ctx, w.ConnectionID)
		if err != nil {
			return "", "", err
		}
		var matching []ScopeOption
		for _, label := range labels {
			if scope == label.ID {
				return label.ID, label.Name, nil
			}
			if strings.EqualFold(scope, label.Name) {
				matching = append(matching, label)
			}
		}
		if len(matching) == 1 {
			return matching[0].ID, matching[0].Name, nil
		}
		return "", "", &tasks.InputError{Message: "Which Gmail label should I watch? Give me a label name or ID from that mailbox."}
	case tasks.GrafanaAlerts, tasks.PrometheusAlerts:
		c, err := s.activeConnection(ctx, w.ConnectionID)
		if err != nil || c.Source != w.Source {
			return "", "", &tasks.InputError{Message: "Choose a connected instance for this watch."}
		}
		if _, err := s.monitoringAlerts(ctx, c); err != nil {
			return "", "", &tasks.InputError{Message: "I couldn’t read alerts from this instance. Check its connection in Connections."}
		}
		return "firing", "Firing alerts", nil
	default:
		return scope, scope, s.ValidateWatchSource(ctx, w)
	}
}

func (s *Service) ReadConnection(ctx context.Context, query tasks.ConnectionQuery) (tasks.ConnectionCard, error) {
	if len(query.Query) > 1000 || len(query.ItemID) > 160 {
		return tasks.ConnectionCard{}, &tasks.InputError{Message: "Use a shorter query or a valid item ID."}
	}
	c, err := s.activeConnection(ctx, query.ConnectionID)
	if err != nil || c.Source != query.Source {
		return tasks.ConnectionCard{}, &tasks.InputError{Message: "That connection is unavailable. Check it in Connections."}
	}
	card := tasks.ConnectionCard{Source: c.Source, Connection: c.Name, ConnectionID: c.ID}
	switch {
	case c.Source == tasks.GmailLabel && (query.Kind == "search" || query.Kind == "message"):
		err = s.readGmailChat(ctx, c, query, &card)
	case (c.Source == tasks.GrafanaAlerts || c.Source == tasks.PrometheusAlerts) && query.Kind == "alerts":
		err = s.readAlertsChat(ctx, c, &card)
	case c.Source == tasks.GrafanaAlerts && query.Kind == "dashboards":
		err = s.readGrafanaDashboards(ctx, c, query.Query, &card)
	case c.Source == tasks.PrometheusAlerts && query.Kind == "query":
		err = s.readPrometheusQuery(ctx, c, query.Query, &card)
	default:
		err = &tasks.InputError{Message: "This connection doesn’t support that lookup."}
	}
	if errors.Is(err, responseError(http.StatusUnauthorized)) || errors.Is(err, responseError(http.StatusForbidden)) {
		err = &tasks.InputError{Message: "The connection refused the lookup. Check its read permissions or reconnect it."}
	}
	return card, err
}

type gmailChatPart struct {
	MIME    string `json:"mimeType"`
	Name    string `json:"filename"`
	Headers []struct{ Name, Value string }
	Body    struct {
		Data       string
		Attachment string `json:"attachmentId"`
	}
	Parts []gmailChatPart
}

type gmailChatMessage struct {
	ID       string
	ThreadID string
	Snippet  string
	LabelIDs []string
	Payload  gmailChatPart
}

func gmailWeb(account string) string {
	if account == "" {
		account = "0"
	}
	return "https://mail.google.com/mail/u/" + url.PathEscape(account) + "/"
}

func (m gmailChatMessage) cardItem(account string) tasks.ConnectionItem {
	item := tasks.ConnectionItem{ID: m.ID, Title: "(No subject)", Body: cleanHeading(m.Snippet, 420)}
	var from, date string
	for _, h := range m.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "subject":
			if subject := cleanHeading(decodeMailHeader(h.Value), 200); subject != "" {
				item.Title = subject
			}
		case "from":
			from = cleanHeading(decodeMailHeader(h.Value), 150)
		case "date":
			date = cleanHeading(h.Value, 100)
		}
	}
	item.Detail = strings.Trim(strings.Join([]string{from, date}, " · "), " ·")
	for _, label := range m.LabelIDs {
		if label == "UNREAD" {
			item.Badge = "Unread"
			break
		}
	}
	ref := m.ThreadID
	if !providerItemID.MatchString(ref) {
		ref = m.ID
	}
	item.URL = gmailWeb(account) + "#all/" + ref
	return item
}

func (s *Service) readGmailChat(ctx context.Context, c tasks.ProviderCredentials, query tasks.ConnectionQuery, card *tasks.ConnectionCard) error {
	token, problem := s.gmailToken(ctx, c)
	if problem != "" {
		return &tasks.InputError{Message: problem}
	}
	read := func(id string, excerpt bool) (gmailChatMessage, error) {
		var m gmailChatMessage
		if !providerItemID.MatchString(id) {
			return m, &tasks.InputError{Message: "Which email should I open? Choose a message from the search results."}
		}
		params := url.Values{"format": {"full"}}
		if !excerpt {
			params.Set("fields", "id,threadId,labelIds,snippet,payload(headers)")
		}
		err := s.getJSON(ctx, gmailAPI+"/messages/"+id+"?"+params.Encode(), token, &m)
		if err == nil && m.ID != id {
			err = errors.New("invalid Gmail message")
		}
		return m, err
	}
	if query.Kind == "message" {
		m, err := read(query.ItemID, true)
		if err != nil {
			return err
		}
		item := m.cardItem(c.Account)
		card.Heading, card.Detail, card.URL, card.ItemID = item.Title, item.Detail, item.URL, item.ID
		card.Body = gmailPartText(m.Payload, "text/plain", 0)
		if card.Body == "" {
			card.Body = gmailPartText(m.Payload, "text/html", 0)
		}
		if card.Body == "" {
			card.Body = item.Body
			card.Warnings = []string{"Only the Gmail search snippet is available. Open Gmail to read the message."}
		}
		return nil
	}
	text := strings.TrimSpace(query.Query)
	if text == "" {
		text = "in:inbox"
	}
	params := url.Values{"q": {text}, "maxResults": {"6"}}
	var page struct {
		NextPageToken string
		Messages      []struct{ ID string }
	}
	if err := s.getJSON(ctx, gmailAPI+"/messages?"+params.Encode(), token, &page); err != nil {
		return err
	}
	card.Heading, card.Detail, card.URL = "Gmail search", text, gmailWeb(c.Account)+"#search/"+url.PathEscape(text)
	card.More = page.NextPageToken != "" || len(page.Messages) > connectionPreviewRows
	for _, ref := range page.Messages[:min(len(page.Messages), connectionPreviewRows)] {
		message, err := read(ref.ID, false)
		if errors.Is(err, responseError(http.StatusNotFound)) {
			continue
		}
		if err != nil {
			return err
		}
		card.Items = append(card.Items, message.cardItem(c.Account))
	}
	if len(card.Items) == 0 {
		card.Body = "No matching messages."
	}
	return nil
}

func (s *Service) readAlertsChat(ctx context.Context, c tasks.ProviderCredentials, card *tasks.ConnectionCard) error {
	alerts, err := s.monitoringAlerts(ctx, c)
	if err != nil {
		return err
	}
	sort.SliceStable(alerts, func(i, j int) bool { return alerts[i].started.After(alerts[j].started) })
	card.Heading, card.URL = "Firing alerts", c.Endpoint+"/alerts"
	if c.Source == tasks.GrafanaAlerts {
		card.URL = c.Endpoint + "/alerting/list"
	}
	card.Detail, card.More = fmt.Sprintf("%d firing", len(alerts)), len(alerts) > connectionPreviewRows
	for _, alert := range alerts[:min(len(alerts), connectionPreviewRows)] {
		name := cleanHeading(alert.labels["alertname"], 180)
		if name == "" {
			name = "Alert"
		}
		var since string
		if !alert.started.IsZero() {
			since = "Since " + alert.started.UTC().Format("Jan 2, 15:04 UTC")
		}
		body := strings.TrimSpace(strings.Join([]string{
			cleanHeading(alert.annotations["summary"], 320), cleanHeading(alert.annotations["description"], 550),
			metricLabels(alert.labels),
		}, "\n"))
		card.Items = append(card.Items, tasks.ConnectionItem{Title: name, Detail: since, Body: body,
			Badge: cleanHeading(alert.labels["severity"], 32)})
	}
	return nil
}

func (s *Service) readGrafanaDashboards(ctx context.Context, c tasks.ProviderCredentials, text string, card *tasks.ConnectionCard) error {
	params := url.Values{"type": {"dash-db"}, "query": {text}, "limit": {"7"}}
	var dashboards []struct {
		UID, Title, FolderTitle string
		OrgID                   int64
	}
	if err := s.getJSON(ctx, c.Endpoint+"/api/search?"+params.Encode(), c.Secret, &dashboards); err != nil {
		return err
	}
	card.Heading, card.Detail, card.URL = "Dashboards", cleanHeading(text, 200), c.Endpoint+"/dashboards"
	card.More = len(dashboards) > connectionPreviewRows
	for _, d := range dashboards[:min(len(dashboards), connectionPreviewRows)] {
		if providerItemID.MatchString(d.UID) {
			link := c.Endpoint + "/d/" + d.UID
			if d.OrgID > 0 {
				link += "?orgId=" + strconv.FormatInt(d.OrgID, 10)
			}
			card.Items = append(card.Items, tasks.ConnectionItem{Title: cleanHeading(d.Title, 200),
				Detail: cleanHeading(d.FolderTitle, 140), URL: link})
		}
	}
	if len(card.Items) == 0 {
		card.Body = "No matching dashboards."
	}
	return nil
}

func metricLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if key != "__name__" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, min(len(keys), 8))
	for _, key := range keys[:min(len(keys), 8)] {
		lines = append(lines, cleanHeading(key, 80)+"="+cleanHeading(labels[key], 150))
	}
	return cleanHeading(strings.Join(lines, " · "), 450)
}

func (s *Service) readPrometheusQuery(ctx context.Context, c tasks.ProviderCredentials, expr string, card *tasks.ConnectionCard) error {
	if strings.TrimSpace(expr) == "" {
		return &tasks.InputError{Message: "Which metric or PromQL expression should I query?"}
	}
	params := url.Values{"query": {expr}, "timeout": {"5s"}, "limit": {"7"}}
	var reply struct {
		Status          string
		Warnings, Infos []string
		Data            struct {
			ResultType string
			Result     json.RawMessage
		}
	}
	err := s.getJSON(ctx, c.Endpoint+"/api/v1/query?"+params.Encode(), c.Secret, &reply)
	if errors.Is(err, responseError(http.StatusBadRequest)) || errors.Is(err, responseError(http.StatusUnprocessableEntity)) {
		return &tasks.InputError{Message: "Prometheus could not evaluate that expression. Check the PromQL and metric names."}
	}
	if err != nil {
		return err
	}
	if reply.Status != "success" {
		return errors.New("invalid Prometheus query response")
	}
	card.Heading, card.Detail, card.URL = "Instant query", cleanHeading(expr, 1000), c.Endpoint+"/graph?"+url.Values{"g0.expr": {expr}}.Encode()
	for _, notes := range [][]string{reply.Warnings, reply.Infos} {
		for _, note := range notes[:min(len(notes), 4)] {
			if note = cleanHeading(note, 300); note != "" {
				card.Warnings = append(card.Warnings, note)
			}
		}
	}
	valueItem := func(title, detail string, sample []json.RawMessage, histogram bool) error {
		var value string
		var at float64
		if len(sample) != 2 || json.Unmarshal(sample[0], &at) != nil {
			return errors.New("invalid Prometheus sample")
		}
		item := tasks.ConnectionItem{Title: title, Detail: detail}
		if histogram {
			var stats struct{ Count, Sum string }
			if err := json.Unmarshal(sample[1], &stats); err != nil || stats.Count == "" || stats.Sum == "" {
				return errors.New("invalid Prometheus histogram")
			}
			item.Badge = "Histogram"
			item.Body = "Count " + cleanHeading(stats.Count, 60) + " · Sum " + cleanHeading(stats.Sum, 60)
		} else {
			if err := json.Unmarshal(sample[1], &value); err != nil {
				return errors.New("invalid Prometheus sample")
			}
			item.Value = cleanHeading(value, 100)
		}
		if card.Body == "" && at > 0 && at < 1e12 {
			card.Body = "Sampled " + time.Unix(int64(at), 0).UTC().Format("Jan 2, 15:04:05 UTC")
		}
		card.Items = append(card.Items, item)
		return nil
	}
	switch reply.Data.ResultType {
	case "vector":
		var series []struct {
			Metric           map[string]string
			Value, Histogram []json.RawMessage
		}
		if err := json.Unmarshal(reply.Data.Result, &series); err != nil {
			return err
		}
		card.More = len(series) > connectionPreviewRows
		for _, sample := range series[:min(len(series), connectionPreviewRows)] {
			title := cleanHeading(sample.Metric["__name__"], 150)
			if title == "" {
				title = "Series"
			}
			value, histogram := sample.Value, len(sample.Histogram) != 0
			if histogram {
				value = sample.Histogram
			}
			if err := valueItem(title, metricLabels(sample.Metric), value, histogram); err != nil {
				return err
			}
		}
	case "scalar", "string":
		var sample []json.RawMessage
		if err := json.Unmarshal(reply.Data.Result, &sample); err != nil {
			return err
		}
		return valueItem("Result", "", sample, false)
	default:
		return &tasks.InputError{Message: "Use an instant expression that returns a number or series values."}
	}
	if len(card.Items) == 0 {
		card.Body = "No matching series."
		if len(card.Warnings) != 0 {
			card.Body = "Prometheus returned no samples. Check the query notices below."
		}
	}
	return nil
}
