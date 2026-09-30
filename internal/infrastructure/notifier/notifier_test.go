package notifier

import (
	"context"
	"errors"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
)

type mockQuotaRepo struct {
	repository.QuotaRepository
	saveNotificationFn func(ctx context.Context, ntf *entity.Notification) error
}

func (m *mockQuotaRepo) SaveNotification(ctx context.Context, ntf *entity.Notification) error {
	if m.saveNotificationFn != nil {
		return m.saveNotificationFn(ctx, ntf)
	}
	return nil
}

func TestInternalNotifier_Send(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		title   string
		message string
		isAlert bool
		setup   func() repository.QuotaRepository
		wantErr bool
	}{
		{
			name:    "Alert notification",
			title:   "Budget exceeded",
			message: "Usage is high",
			isAlert: true,
			setup: func() repository.QuotaRepository {
				return &mockQuotaRepo{
					saveNotificationFn: func(ctx context.Context, ntf *entity.Notification) error {
						if ntf.Type != entity.NotificationTypeAlert {
							t.Errorf("expected type %v, got %v", entity.NotificationTypeAlert, ntf.Type)
						}
						if ntf.IsAlert != true {
							t.Errorf("expected IsAlert true, got false")
						}
						return nil
					},
				}
			},
			wantErr: false,
		},
		{
			name:    "Report notification with 月次",
			title:   "月次レポート",
			message: "Report details",
			isAlert: false,
			setup: func() repository.QuotaRepository {
				return &mockQuotaRepo{
					saveNotificationFn: func(ctx context.Context, ntf *entity.Notification) error {
						if ntf.Type != entity.NotificationTypeReport {
							t.Errorf("expected type %v, got %v", entity.NotificationTypeReport, ntf.Type)
						}
						return nil
					},
				}
			},
			wantErr: false,
		},
		{
			name:    "Report notification with Report keyword",
			title:   "Monthly Report",
			message: "Report details",
			isAlert: false,
			setup: func() repository.QuotaRepository {
				return &mockQuotaRepo{
					saveNotificationFn: func(ctx context.Context, ntf *entity.Notification) error {
						if ntf.Type != entity.NotificationTypeReport {
							t.Errorf("expected type %v, got %v", entity.NotificationTypeReport, ntf.Type)
						}
						return nil
					},
				}
			},
			wantErr: false,
		},
		{
			name:    "Info notification",
			title:   "Just an info",
			message: "Info details",
			isAlert: false,
			setup: func() repository.QuotaRepository {
				return &mockQuotaRepo{
					saveNotificationFn: func(ctx context.Context, ntf *entity.Notification) error {
						if ntf.Type != entity.NotificationTypeInfo {
							t.Errorf("expected type %v, got %v", entity.NotificationTypeInfo, ntf.Type)
						}
						return nil
					},
				}
			},
			wantErr: false,
		},
		{
			name:    "Nil repository",
			title:   "No repo",
			message: "Should not crash",
			isAlert: false,
			setup: func() repository.QuotaRepository {
				return nil
			},
			wantErr: false,
		},
		{
			name:    "Repository save error",
			title:   "Error case",
			message: "Failed to save",
			isAlert: false,
			setup: func() repository.QuotaRepository {
				return &mockQuotaRepo{
					saveNotificationFn: func(ctx context.Context, ntf *entity.Notification) error {
						return errors.New("save error")
					},
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := tt.setup()
			n := NewInternalNotifier(repo)

			err := n.Send(ctx, tt.title, tt.message, tt.isAlert)
			if (err != nil) != tt.wantErr {
				t.Errorf("Send() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLogNotifier_Send(t *testing.T) {
	ctx := context.Background()
	n := NewLogNotifier()

	// Should not return any error for info
	err := n.Send(ctx, "Test Log Info", "Test Message Info", false)
	if err != nil {
		t.Errorf("LogNotifier.Send() unexpected error = %v", err)
	}

	// Should not return any error for alert
	err = n.Send(ctx, "Test Log Alert", "Test Message Alert", true)
	if err != nil {
		t.Errorf("LogNotifier.Send() unexpected error = %v", err)
	}
}
