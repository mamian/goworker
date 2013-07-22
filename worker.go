package goworker

import (
	"code.google.com/p/vitess/go/pools"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type worker struct {
	process
}

func newWorker(id string, queues []string) (*worker, error) {
	process, err := newProcess(id, queues)
	if err != nil {
		return nil, err
	}
	return &worker{
		process: *process,
	}, nil
}

func (w *worker) MarshalJSON() ([]byte, error) {
	return json.Marshal(w.String())
}

func (w *worker) start(conn *redisConn, job *job) error {
	work := &work{
		Queue:   job.Queue,
		RunAt:   time.Now(),
		Payload: job.Payload,
	}

	buffer, err := json.Marshal(work)
	if err != nil {
		return err
	}

	conn.Send("SET", fmt.Sprintf("resque:worker:%s", w), buffer)
	logger.Debugf("Processing %s since %s [%v]", work.Queue, work.RunAt, work.Payload.Class)

	return w.process.start(conn)
}

func (w *worker) fail(conn *redisConn, job *job, err error) error {
	failure := &failure{
		FailedAt:  time.Now(),
		Payload:   job.Payload,
		Exception: "Error",
		Error:     err.Error(),
		Worker:    w,
		Queue:     job.Queue,
	}
	buffer, err := json.Marshal(failure)
	if err != nil {
		return err
	}
	conn.Send("RPUSH", "resque:failed", buffer)

	return w.process.fail(conn)
}

func (w *worker) succeed(conn *redisConn, job *job) error {
	conn.Send("INCR", "resque:stat:processed")
	conn.Send("INCR", fmt.Sprintf("resque:stat:processed:%s", w))

	return nil
}

func (w *worker) finish(conn *redisConn, job *job, err error) error {
	if err != nil {
		w.fail(conn, job, err)
	} else {
		w.succeed(conn, job)
	}
	return w.process.finish(conn)
}

func (w *worker) run(pool *pools.ResourcePool, job *job, workerFunc WorkerFunc) {
	var err error
	defer func() {
		resource, err := pool.Get()
		if err != nil {
			logger.Criticalf("Error on getting connection in worker %v", w)
		} else {
			conn := resource.(*redisConn)
			w.finish(conn, job, err)
			pool.Put(conn)
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			err = errors.New(fmt.Sprint(r))
		}
	}()

	resource, err := pool.Get()
	if err != nil {
		logger.Criticalf("Error on getting connection in worker %v", w)
	} else {
		conn := resource.(*redisConn)
		w.start(conn, job)
		pool.Put(conn)
	}
	err = workerFunc(job.Queue, job.Payload.Args...)
}
