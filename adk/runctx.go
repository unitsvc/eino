/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package adk

import (
	"context"
	"sync"
)

type runSession struct {
	Events []*agentEventWrapper
	Values map[string]any

	interruptRunCtxs []*runContext // won't consider concurrency now

	mtx sync.Mutex
}

type agentEventWrapper struct {
	*AgentEvent
	mu                  sync.Mutex
	concatenatedMessage Message
}

func newRunSession() *runSession {
	return &runSession{
		Values: make(map[string]any),
	}
}

func getInterruptRunCtxs(ctx context.Context) []*runContext {
	session := getSession(ctx)
	if session == nil {
		return nil
	}
	return session.getInterruptRunCtxs()
}

func appendInterruptRunCtx(ctx context.Context, interruptRunCtx *runContext) {
	session := getSession(ctx)
	if session == nil {
		return
	}
	session.appendInterruptRunCtx(interruptRunCtx)
}

func replaceInterruptRunCtx(ctx context.Context, interruptRunCtx *runContext) {
	session := getSession(ctx)
	if session == nil {
		return
	}
	session.replaceInterruptRunCtx(interruptRunCtx)
}

func GetSessionValues(ctx context.Context) map[string]any {
	session := getSession(ctx)
	if session == nil {
		return map[string]any{}
	}

	return session.getValues()
}

func SetSessionValue(ctx context.Context, key string, value any) {
	session := getSession(ctx)
	if session == nil {
		return
	}

	session.setValue(key, value)
}

func GetSessionValue(ctx context.Context, key string) (any, bool) {
	session := getSession(ctx)
	if session == nil {
		return nil, false
	}

	return session.getValue(key)
}

func (rs *runSession) addEvent(event *AgentEvent) {
	rs.mtx.Lock()
	rs.Events = append(rs.Events, &agentEventWrapper{
		AgentEvent: event,
	})
	rs.mtx.Unlock()
}

func (rs *runSession) getEvents() []*agentEventWrapper {
	rs.mtx.Lock()
	events := rs.Events
	rs.mtx.Unlock()

	return events
}

func (rs *runSession) getInterruptRunCtxs() []*runContext {
	rs.mtx.Lock()
	defer rs.mtx.Unlock()
	return rs.interruptRunCtxs
}

func (rs *runSession) appendInterruptRunCtx(runCtx *runContext) {
	rs.mtx.Lock()
	rs.interruptRunCtxs = append(rs.interruptRunCtxs, runCtx)
	rs.mtx.Unlock()
}

func (rs *runSession) replaceInterruptRunCtx(interruptRunCtx *runContext) {
	// remove runctx whose path is belong to the new run ctx, and append the new run ctx
	rs.mtx.Lock()
	for i := 0; i < len(rs.interruptRunCtxs); i++ {
		rc := rs.interruptRunCtxs[i]
		if belongToRunPath(interruptRunCtx.RunPath, rc.RunPath) {
			rs.interruptRunCtxs = append(rs.interruptRunCtxs[:i], rs.interruptRunCtxs[i+1:]...)
			i--
		}
	}
	rs.interruptRunCtxs = append(rs.interruptRunCtxs, interruptRunCtx)
	rs.mtx.Unlock()
}

func (rs *runSession) getValues() map[string]any {
	rs.mtx.Lock()
	values := make(map[string]any, len(rs.Values))
	for k, v := range rs.Values {
		values[k] = v
	}
	rs.mtx.Unlock()

	return values
}

func (rs *runSession) setValue(key string, value any) {
	rs.mtx.Lock()
	rs.Values[key] = value
	rs.mtx.Unlock()
}

func (rs *runSession) getValue(key string) (any, bool) {
	rs.mtx.Lock()
	value, ok := rs.Values[key]
	rs.mtx.Unlock()

	return value, ok
}

type runContext struct {
	RootInput *AgentInput
	RunPath   []string

	Session *runSession
}

func (rc *runContext) isRoot() bool {
	return len(rc.RunPath) == 1
}

func (rc *runContext) deepCopy() *runContext {
	copied := &runContext{
		RootInput: rc.RootInput,
		RunPath:   make([]string, len(rc.RunPath)),
		Session:   rc.Session,
	}

	copy(copied.RunPath, rc.RunPath)

	return copied
}

type runCtxKey struct{}

func getRunCtx(ctx context.Context) *runContext {
	runCtx, ok := ctx.Value(runCtxKey{}).(*runContext)
	if !ok {
		return nil
	}
	return runCtx
}

func setRunCtx(ctx context.Context, runCtx *runContext) context.Context {
	return context.WithValue(ctx, runCtxKey{}, runCtx)
}

func initRunCtx(ctx context.Context, agentName string, input *AgentInput) (context.Context, *runContext) {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		runCtx = runCtx.deepCopy()
	} else {
		runCtx = &runContext{Session: newRunSession()}
	}

	runCtx.RunPath = append(runCtx.RunPath, agentName)
	if runCtx.isRoot() {
		runCtx.RootInput = input
	}

	return setRunCtx(ctx, runCtx), runCtx
}

func ClearRunCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, runCtxKey{}, nil)
}

func ctxWithNewRunCtx(ctx context.Context) context.Context {
	return setRunCtx(ctx, &runContext{Session: newRunSession()})
}

func getSession(ctx context.Context) *runSession {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		return runCtx.Session
	}

	return nil
}
