package automations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"buddy/internal/tasks"
)

type firingAlert struct {
	labels, annotations map[string]string
	started             time.Time
}

func (s *Service) monitoringAlerts(ctx context.Context, c tasks.ProviderCredentials) ([]firingAlert, error) {
	var active []firingAlert
	switch c.Source {
	case tasks.PrometheusAlerts:
		var reply struct {
			Status string
			Data   struct {
				Alerts []struct {
					State               string
					Labels, Annotations map[string]string
					ActiveAt            time.Time
				}
			}
		}
		if err := s.getJSON(ctx, c.Endpoint+"/api/v1/alerts", c.Secret, &reply); err != nil {
			return nil, err
		}
		if reply.Status != "success" {
			return nil, errors.New("invalid Prometheus alerts response")
		}
		for _, a := range reply.Data.Alerts {
			if a.State == "firing" {
				active = append(active, firingAlert{a.Labels, a.Annotations, a.ActiveAt})
			}
		}
	case tasks.GrafanaAlerts:
		var reply []struct {
			Status              struct{ State string }
			Labels, Annotations map[string]string
			StartsAt            time.Time
		}
		const alerts = "/api/alertmanager/grafana/api/v2/alerts?active=true&silenced=false&inhibited=false&unprocessed=false"
		if err := s.getJSON(ctx, c.Endpoint+alerts, c.Secret, &reply); err != nil {
			return nil, err
		}
		if reply == nil {
			return nil, errors.New("invalid Grafana alerts response")
		}
		for _, a := range reply {
			if a.Status.State == "active" {
				active = append(active, firingAlert{a.Labels, a.Annotations, a.StartsAt})
			}
		}
	default:
		return nil, errors.New("unsupported monitoring service")
	}
	return active, nil
}

func (s *Service) pollMonitoring(ctx context.Context, c tasks.ProviderCredentials, w tasks.Watch) string {
	alerts, err := s.monitoringAlerts(ctx, c)
	if err != nil {
		return err.Error()
	}
	for _, alert := range alerts {
		if len(alert.labels) == 0 || alert.started.IsZero() {
			continue
		}
		labels, _ := json.Marshal(alert.labels)
		key := sha256.Sum256([]byte(string(labels) + "\n" + alert.started.UTC().Format(time.RFC3339Nano)))
		name := cleanHeading(alert.labels["alertname"], 200)
		if name == "" {
			name = "Monitoring alert"
		}
		var lines []string
		for label, value := range alert.labels {
			lines = append(lines, cleanHeading(label, 80)+"="+cleanHeading(value, 250))
		}
		sort.Strings(lines)
		detail := c.Name + "\n" + cleanHeading(alert.annotations["summary"], 500) + "\n" +
			cleanHeading(alert.annotations["description"], 1800) + "\n" + cleanHeading(strings.Join(lines, ", "), 4000)
		sourceURL := ""
		if strings.HasPrefix(c.Endpoint, "https://") {
			sourceURL = c.Endpoint + "/alerts"
			if c.Source == tasks.GrafanaAlerts {
				sourceURL = c.Endpoint + "/alerting/list"
			}
		}
		_, err := s.store.RecordStandingSignal(ctx, tasks.StandingSignal{
			Source: w.Source, ConnectionID: c.ID, Scope: w.Scope, Key: hex.EncodeToString(key[:]),
			Title: name + " is firing", Detail: detail, URL: sourceURL,
			Alert: name, Severity: cleanHeading(alert.labels["severity"], 100),
			OccurredAt: alert.started,
		})
		if err != nil {
			return "Couldn’t save a firing alert."
		}
	}
	return ""
}
