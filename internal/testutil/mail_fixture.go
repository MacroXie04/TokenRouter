package testutil

import (
	"sync"
)

type RecordingMailer struct {
	sync.Mutex
	To      []string
	Subject []string
	Err     error
}

func (m *RecordingMailer) Send(to, subject, body string) error {
	m.Lock()
	defer m.Unlock()
	m.To = append(m.To, to)
	m.Subject = append(m.Subject, subject)
	return m.Err
}
