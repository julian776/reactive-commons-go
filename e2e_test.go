package rcgo

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/suite"
)

// This test suite contains all tests that require a running server.
type E2ETestSuite struct {
	suite.Suite
	ctx context.Context
	url string

	lApp string
	l    *Listener

	pApp string
	p    *Publisher
}

func (s *E2ETestSuite) SetupSuite() {
	s.url = "amqp://user:password@localhost"
	s.lApp = "testingListenerApp"
	s.pApp = "testingPublisherApp"
	ctx, _ := context.WithTimeout(context.Background(), time.Second*10)
	s.ctx = ctx

	_, err := amqp.Dial(s.url)
	if err != nil {
		s.FailNow("Failed to connect to RabbitMQ can not run e2e test")

		return
	}

	pconfigs := NewPublisherDefaultConfigs(s.url)
	pconfigs.ReplyTimeout = time.Second * 45
	s.p = NewPublisher(pconfigs, s.pApp)
}

func TestE2ETestSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	suite.Run(t, new(E2ETestSuite))
}

func (s *E2ETestSuite) TearDownSuite() {
	err := s.p.Stop()
	if err != nil {
		s.T().Logf("error: %s", err.Error())
	}
}

func (s *E2ETestSuite) TestE2E_Cmds() {
	cmdTyp := fmt.Sprintf("%s.%s", s.lApp, "cmd")
	data := map[string]interface{}{
		"data": "data",
	}
	calls := 0

	lconfigs := NewListenerDefaultConfigs(s.url)
	lconfigs.LogLevel = "disabled"
	l := NewListener(lconfigs, s.lApp)

	wg := sync.WaitGroup{}
	wg.Add(2)

	l.AddCommandHandler(
		cmdTyp,
		func(ctx context.Context, c *Cmd) error {
			s.Len(c.Id, 36)
			s.Equal(s.pApp, c.Source)
			s.Equal(s.lApp, c.Target)
			s.Equal(cmdTyp, c.Type)
			s.WithinDuration(time.Now(), c.GenerationTime, time.Second*1)
			s.Exactly(data, c.Data)

			calls++
			wg.Done()
			return nil
		})

	err := l.Listen(s.ctx)
	s.Nil(err)

	// Provide sufficient time for the listener to start.
	time.Sleep(time.Microsecond * 150)

	err = s.p.SendCmd(s.ctx, s.lApp, cmdTyp, data)
	s.Nil(err)

	err = s.p.SendCmd(s.ctx, s.lApp, cmdTyp, data)
	s.Nil(err)

	wg.Wait()

	s.Equal(2, calls)

	err = l.Stop()
	s.Nil(err)
}

func (s *E2ETestSuite) TestE2E_Events() {
	eventTyp := "orderPlaced"
	data := map[string]interface{}{
		"data": "data",
	}
	calls := 0

	// These ids are used to ensure that both
	// handlers receive the same id.
	var id1 string
	var id2 string

	lconfigs := NewListenerDefaultConfigs(s.url)
	lconfigs.LogLevel = "disabled"
	l := NewListener(lconfigs, s.lApp)

	wg := sync.WaitGroup{}
	wg.Add(2)

	l.AddEventHandler(
		eventTyp,
		func(ctx context.Context, e *Event) error {
			s.Len(e.Id, 36)
			id1 = e.Id
			s.Equal(s.pApp, e.Source)
			s.Equal(eventTyp, e.Type)
			s.WithinDuration(time.Now(), e.GenerationTime, time.Second*1)
			s.Exactly(data, e.Data)

			calls++
			wg.Done()
			return nil
		})

	err := l.Listen(s.ctx)
	s.Nil(err)

	// In this test, we generate an additional listener
	// to verify the broadcast functionality of events
	// to all apps that register for any event.
	configsOtherListener := NewListenerDefaultConfigs(s.url)
	configsOtherListener.LogLevel = "disabled"
	otherListener := NewListener(configsOtherListener, "otherListener")

	otherListener.AddEventHandler(
		eventTyp,
		func(ctx context.Context, e *Event) error {
			s.Len(e.Id, 36)
			id2 = e.Id
			s.Equal(s.pApp, e.Source)
			s.Equal(eventTyp, e.Type)
			s.WithinDuration(time.Now(), e.GenerationTime, time.Second*1)
			s.Exactly(data, e.Data)

			calls++
			wg.Done()
			return nil
		})

	err = otherListener.Listen(s.ctx)
	s.Nil(err)

	// Provide sufficient time for the listener to start.
	time.Sleep(time.Millisecond * 100)

	err = s.p.PublishEvent(s.ctx, eventTyp, data)
	s.Nil(err)

	wg.Wait()

	s.Exactly(id1, id2)
	s.Equal(2, calls)

	err = l.Stop()
	s.Nil(err)

	err = otherListener.Stop()
	s.Nil(err)
}

func (s *E2ETestSuite) TestE2E_Queries() {
	queryTyp := fmt.Sprintf("%s.%s", s.lApp, "query")
	data := map[string]interface{}{
		"data": "data",
	}
	dataRes := map[string]interface{}{
		"data": "data",
	}
	calls := 0

	lconfigs := NewListenerDefaultConfigs(s.url)
	lconfigs.LogLevel = "disabled"
	l := NewListener(lconfigs, s.lApp)

	wg := sync.WaitGroup{}
	wg.Add(2)

	l.AddQueryHandler(
		queryTyp,
		func(ctx context.Context, q *Query) (interface{}, error) {
			s.Equal(queryTyp, q.Target)
			s.Equal(queryTyp, q.Type)
			s.WithinDuration(time.Now(), q.GenerationTime, time.Second*1)
			s.Exactly(data, q.Data)

			calls++
			wg.Done()

			return dataRes, nil
		})

	err := l.Listen(s.ctx)
	s.Nil(err)

	// Provide sufficient time for the listener to start.
	time.Sleep(time.Millisecond * 100)

	res1, err := s.p.RequestReply(s.ctx, s.lApp, queryTyp, data)
	s.Nil(err)
	if err != nil {
		// unblock if err
		wg.Done()
	}

	res2, err := s.p.RequestReply(s.ctx, s.lApp, queryTyp, data)
	s.Nil(err)
	if err != nil {
		// unblock if err
		wg.Done()
	}

	wg.Wait()

	dataResJson, _ := json.Marshal(dataRes)
	s.Exactly(dataResJson, res1)

	dataResJson, _ = json.Marshal(dataRes)
	s.Exactly(dataResJson, res2)

	s.Equal(2, calls)
	err = l.Stop()
	s.Nil(err)
}

func (s *E2ETestSuite) TestE2E_Close() {
	// create a new listener to this test
	lconfigs := NewListenerDefaultConfigs(s.url)
	lconfigs.LogLevel = "disabled"
	s.l = NewListener(lconfigs, s.lApp)
	go s.l.Listen(s.ctx)

	// Provide sufficient time for the listener to start.
	time.Sleep(time.Millisecond * 100)

	err := s.l.Stop()
	s.Nil(err)
}

func (s *E2ETestSuite) TestPublisher_StartAfterStop() {
	s.p.Start(context.Background())

	// Stop the publisher
	err := s.p.Stop()
	s.Nil(err)

	// Try to start the publisher again
	err = s.p.Start(context.Background())
	s.Nil(err)

	// Stop the publisher
	err = s.p.Stop()
	s.Nil(err)

	// Try to start the publisher again
	err = s.p.Start(context.Background())
	s.Nil(err)
}

func (s *E2ETestSuite) TestListener_StartAfterStop() {
	lconfigs := NewListenerDefaultConfigs(s.url)
	lconfigs.LogLevel = "disabled"
	l := NewListener(lconfigs, s.lApp)

	// Stop the listener
	err := l.Stop()
	s.Nil(err)

	// Try to start the listener again
	err = l.Listen(context.Background())
	s.Nil(err)

	// Stop the listener
	err = l.Stop()
	s.Nil(err)

	// Try to start the listener again
	err = l.Listen(context.Background())
	s.Nil(err)
}
