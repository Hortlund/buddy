package automations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"buddy/internal/tasks"
)

const gmailAPI = "https://gmail.googleapis.com/gmail/v1/users/me"

type gmailProgress struct {
	History string `json:"history"`
	Page    string `json:"page,omitempty"`
	Resync  bool   `json:"resync,omitempty"`
	Initial bool   `json:"initial,omitempty"`
}

type gmailTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (s *Service) gmailToken(ctx context.Context, c tasks.ProviderCredentials) (string, string) {
	reply, err := s.exchangeGmail(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID},
		"client_secret": {c.ClientSecret}, "refresh_token": {c.Secret}})
	if err != nil {
		return "", err.Error()
	}
	return reply.AccessToken, ""
}

func (s *Service) exchangeGmail(ctx context.Context, form url.Values) (gmailTokens, error) {
	var reply gmailTokens
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return reply, errors.New("couldn’t prepare Gmail sign-in")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.client.Do(req)
	if err != nil {
		return reply, errors.New("Gmail sign-in is unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&reply) != nil || reply.AccessToken == "" {
		return gmailTokens{}, errors.New("Gmail authorization expired or failed. Reconnect this account.")
	}
	return reply, nil
}

func (s *Service) pollGmail(ctx context.Context, token string, w tasks.Watch, p tasks.PollState) (string, string) {
	progress := gmailProgress{History: p.Cursor}
	if strings.HasPrefix(p.Cursor, "{") {
		if err := json.Unmarshal([]byte(p.Cursor), &progress); err != nil {
			return "", "Invalid saved Gmail progress."
		}
	}
	if progress.Resync {
		return s.gmailResync(ctx, token, w, p, progress)
	}
	if progress.History == "" {
		return s.startGmailResync(ctx, token, w, p, true)
	}
	query := url.Values{"startHistoryId": {progress.History}, "labelId": {w.Scope},
		"historyTypes": {"messageAdded", "labelAdded"}, "maxResults": {"100"}}
	type reference struct {
		ID string `json:"id"`
	}
	for range 10 {
		if progress.Page != "" {
			query.Set("pageToken", progress.Page)
		}
		var reply struct {
			HistoryID string `json:"historyId"`
			Next      string `json:"nextPageToken"`
			History   []struct {
				ID    string `json:"id"`
				Added []struct {
					Message reference `json:"message"`
				} `json:"messagesAdded"`
				Labeled []struct {
					Message reference `json:"message"`
					Labels  []string  `json:"labelIds"`
				} `json:"labelsAdded"`
			} `json:"history"`
		}
		err := s.getJSON(ctx, gmailAPI+"/history?"+query.Encode(), token, &reply)
		if progress.Page != "" && (errors.Is(err, responseError(http.StatusBadRequest)) || errors.Is(err, responseError(http.StatusNotFound))) {
			progress.Page = ""
			return s.checkpointGmail(ctx, p, progress, "Gmail will refresh its expired page token.")
		}
		if errors.Is(err, responseError(http.StatusNotFound)) {
			return s.startGmailResync(ctx, token, w, p, false)
		}
		if err != nil {
			return "", "Gmail: " + err.Error()
		}
		for _, event := range reply.History {
			for _, entry := range event.Added {
				if err := s.gmailMessage(ctx, token, w, entry.Message.ID, event.ID, false); err != nil {
					return "", "Gmail: " + err.Error()
				}
			}
			for _, entry := range event.Labeled {
				if !slices.Contains(entry.Labels, w.Scope) {
					continue
				}
				if err := s.gmailMessage(ctx, token, w, entry.Message.ID, event.ID, true); err != nil {
					return "", "Gmail: " + err.Error()
				}
			}
		}
		if reply.Next == "" {
			if reply.HistoryID == "" {
				return "", "Gmail returned no history cursor."
			}
			return reply.HistoryID, ""
		}
		progress.Page = reply.Next
		if _, problem := s.checkpointGmail(ctx, p, progress, ""); problem != "" {
			return "", problem
		}
	}
	return "", "Catching up with Gmail. Progress is saved."
}

func (s *Service) startGmailResync(ctx context.Context, token string, w tasks.Watch, p tasks.PollState, initial bool) (string, string) {
	var profile struct {
		HistoryID string `json:"historyId"`
	}
	if err := s.getJSON(ctx, gmailAPI+"/profile", token, &profile); err != nil || profile.HistoryID == "" {
		return "", "Couldn’t read the Gmail history cursor."
	}
	progress := gmailProgress{History: profile.HistoryID, Resync: true, Initial: initial}
	if _, problem := s.checkpointGmail(ctx, p, progress, ""); problem != "" {
		return "", problem
	}
	return s.gmailResync(ctx, token, w, p, progress)
}

func (s *Service) gmailResync(ctx context.Context, token string, w tasks.Watch, p tasks.PollState, progress gmailProgress) (string, string) {
	query := url.Values{"labelIds": {w.Scope}, "maxResults": {"100"}, "includeSpamTrash": {"true"}}
	if progress.Initial {
		query.Set("q", "after:"+strconv.FormatInt(w.ActiveSince.Unix(), 10))
	}
	for range 10 {
		if progress.Page != "" {
			query.Set("pageToken", progress.Page)
		}
		var reply struct {
			Next     string `json:"nextPageToken"`
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		}
		if err := s.getJSON(ctx, gmailAPI+"/messages?"+query.Encode(), token, &reply); err != nil {
			if progress.Page != "" && (errors.Is(err, responseError(http.StatusBadRequest)) || errors.Is(err, responseError(http.StatusNotFound))) {
				progress.Page = ""
				return s.checkpointGmail(ctx, p, progress, "Gmail will refresh its expired page token.")
			}
			return "", "Gmail: " + err.Error()
		}
		for _, m := range reply.Messages {
			if err := s.gmailMessage(ctx, token, w, m.ID, "", false); err != nil {
				return "", "Gmail: " + err.Error()
			}
		}
		if reply.Next == "" {
			return progress.History, ""
		}
		progress.Page = reply.Next
		if _, problem := s.checkpointGmail(ctx, p, progress, ""); problem != "" {
			return "", problem
		}
	}
	return "", "Resyncing the Gmail label. Progress is saved."
}

func (s *Service) checkpointGmail(ctx context.Context, p tasks.PollState, progress gmailProgress, status string) (string, string) {
	data, _ := json.Marshal(progress)
	if err := s.store.CheckpointStandingPoll(ctx, p, string(data)); err != nil {
		return "", "Couldn’t save Gmail progress."
	}
	return "", status
}

func (s *Service) gmailMessage(ctx context.Context, token string, w tasks.Watch, id, history string, labelAdded bool) error {
	if id == "" {
		return nil
	}
	if seen, err := s.store.StandingSignalExists(ctx, tasks.WatchSourceKey(w.Source, w.ConnectionID), w.Scope, id); err != nil || seen {
		return err
	}
	var message struct {
		Labels       []string `json:"labelIds"`
		Snippet      string   `json:"snippet"`
		InternalDate string   `json:"internalDate"`
		Payload      struct {
			Headers []struct{ Name, Value string } `json:"headers"`
		} `json:"payload"`
	}
	query := url.Values{"format": {"metadata"}, "metadataHeaders": {"Subject", "From"}}
	err := s.getJSON(ctx, gmailAPI+"/messages/"+url.PathEscape(id)+"?"+query.Encode(), token, &message)
	if errors.Is(err, responseError(http.StatusNotFound)) {
		return nil // Removed before the watch could read it.
	}
	if err != nil {
		return err
	}
	present := false
	for _, value := range message.Labels {
		present = present || value == w.Scope
	}
	if !present {
		return nil
	}
	title, sender, senderAddress, subject := "Gmail message", "", "", ""
	for _, header := range message.Payload.Headers {
		switch strings.ToLower(header.Name) {
		case "subject":
			subject = decodeMailHeader(header.Value)
			if preview := cleanHeading(subject, 220); preview != "" {
				title = preview
			}
		case "from":
			sender = decodeMailHeader(header.Value)
			parser := mail.AddressParser{WordDecoder: &mailWordDecoder}
			if address, err := parser.Parse(header.Value); err == nil {
				senderAddress = address.Address
			}
		}
	}
	mailbox := "0"
	if w.ConnectionID != "" {
		if c, err := s.store.ProviderCredential(ctx, w.ConnectionID); err == nil && c.Account != "" {
			mailbox = c.Account
		}
	}
	var occurredAt time.Time
	if !labelAdded {
		if millis, err := strconv.ParseInt(message.InternalDate, 10, 64); err == nil && millis > 0 {
			occurredAt = time.UnixMilli(millis)
		}
	}
	_, err = s.store.RecordStandingSignal(ctx, tasks.StandingSignal{
		Source: tasks.GmailLabel, ConnectionID: w.ConnectionID, Scope: w.Scope, Key: id, Title: title,
		Sender: senderAddress, Subject: subject, OccurredAt: occurredAt, GmailHistory: history,
		URL:    "https://mail.google.com/mail/u/" + url.PathEscape(mailbox) + "/#all/" + url.PathEscape(id),
		Detail: "From: " + cleanHeading(sender, 300) + "\n" + cleanHeading(message.Snippet, 1800),
	})
	return err
}
