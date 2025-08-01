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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
	ub "github.com/cloudwego/eino/utils/callbacks"
)

type chatModelAgentRunOptions struct {
	// run
	chatModelOptions []model.Option
	toolOptions      []tool.Option
	agentToolOptions map[ /*tool name*/ string][]AgentRunOption // todo: map or list?

	// resume
	historyModifier func(context.Context, []Message) []Message
}

func WithChatModelOptions(opts []model.Option) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.chatModelOptions = opts
	})
}

func WithToolOptions(opts []tool.Option) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.toolOptions = opts
	})
}

func WithAgentToolRunOptions(opts map[string] /*tool name*/ []AgentRunOption) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.agentToolOptions = opts
	})
}

func WithHistoryModifier(f func(context.Context, []Message) []Message) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.historyModifier = f
	})
}

type ToolsConfig struct {
	compose.ToolsNodeConfig

	// Names of the tools that will make agent return directly when the tool is called.
	// When multiple tools are called and more than one tool is in the return directly list, only the first one will be returned.
	ReturnDirectly map[string]bool
}

type GenModelInput func(ctx context.Context, instruction string, input *AgentInput) ([]Message, error)

func defaultGenModelInput(ctx context.Context, instruction string, input *AgentInput) ([]Message, error) {
	msgs := make([]Message, 0, len(input.Messages)+1)

	if instruction != "" {
		sp := schema.SystemMessage(instruction)

		vs := GetSessionValues(ctx)
		if len(vs) > 0 {
			ct := prompt.FromMessages(schema.FString, sp)
			ms, err := ct.Format(ctx, vs)
			if err != nil {
				return nil, err
			}

			sp = ms[0]
		}

		msgs = append(msgs, sp)
	}

	msgs = append(msgs, input.Messages...)

	return msgs, nil
}

type ChatModelAgentConfig struct {
	Name        string
	Description string
	Instruction string

	Model model.ToolCallingChatModel

	ToolsConfig ToolsConfig

	// optional
	GenModelInput GenModelInput

	// Exit tool. Optional, defaults to nil, which will generate an Exit Action.
	// The built-in implementation is 'ExitTool'
	Exit tool.BaseTool

	// optional
	OutputKey string

	MaxStep int
}

type ChatModelAgent struct {
	name        string
	description string
	instruction string

	model       model.ToolCallingChatModel
	toolsConfig ToolsConfig

	genModelInput GenModelInput

	outputKey string
	maxStep   int

	subAgents   []Agent
	parentAgent Agent

	disallowTransferToParent bool

	exit tool.BaseTool

	// runner
	once   sync.Once
	run    runFunc
	frozen uint32
}

type runFunc func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *mockStore, opts ...compose.Option)

var registerInternalTypeOnce sync.Once

func NewChatModelAgent(_ context.Context, config *ChatModelAgentConfig) (*ChatModelAgent, error) {
	if config.Name == "" {
		return nil, errors.New("agent 'Name' is required")
	}
	if config.Description == "" {
		return nil, errors.New("agent 'Description' is required")
	}
	if config.Model == nil {
		return nil, errors.New("agent 'Model' is required")
	}

	var err error
	registerInternalTypeOnce.Do(func() {
		err = compose.RegisterInternalType(func(key string, value any) error {
			gob.RegisterName(key, value)
			return nil
		})
		gob.RegisterName("_eino_message", &schema.Message{})
		gob.RegisterName("_eino_document", &schema.Document{})
		gob.RegisterName("_eino_tool_call", schema.ToolCall{})
		gob.RegisterName("_eino_function_call", schema.FunctionCall{})
		gob.RegisterName("_eino_response_meta", &schema.ResponseMeta{})
		gob.RegisterName("_eino_token_usage", &schema.TokenUsage{})
		gob.RegisterName("_eino_log_probs", &schema.LogProbs{})
		gob.RegisterName("_eino_chat_message_part", schema.ChatMessagePart{})
		gob.RegisterName("_eino_chat_message_image_url", &schema.ChatMessageImageURL{})
		gob.RegisterName("_eino_chat_message_audio_url", &schema.ChatMessageAudioURL{})
		gob.RegisterName("_eino_chat_message_video_url", &schema.ChatMessageVideoURL{})
		gob.RegisterName("_eino_chat_message_file_url", &schema.ChatMessageFileURL{})
	})
	if err != nil {
		return nil, err
	}

	genInput := defaultGenModelInput
	if config.GenModelInput != nil {
		genInput = config.GenModelInput
	}

	return &ChatModelAgent{
		name:          config.Name,
		description:   config.Description,
		instruction:   config.Instruction,
		model:         config.Model,
		toolsConfig:   config.ToolsConfig,
		genModelInput: genInput,
		exit:          config.Exit,
		outputKey:     config.OutputKey,
		maxStep:       config.MaxStep,
	}, nil
}

const (
	TransferToAgentToolName = "transfer_to_agent"
	TransferToAgentToolDesc = "Transfer the question to another agent."
)

var (
	toolInfoTransferToAgent = &schema.ToolInfo{
		Name: TransferToAgentToolName,
		Desc: TransferToAgentToolDesc,

		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"agent_name": {
				Desc:     "the name of the agent to transfer to",
				Required: true,
				Type:     schema.String,
			},
		}),
	}

	ToolInfoExit = &schema.ToolInfo{
		Name: "exit",
		Desc: "Exit the agent process and return the final result.",

		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"final_result": {
				Desc:     "the final result to return",
				Required: true,
				Type:     schema.String,
			},
		}),
	}
)

type ExitTool struct{}

func (et ExitTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return ToolInfoExit, nil
}

func (et ExitTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type exitParams struct {
		FinalResult string `json:"final_result"`
	}

	params := &exitParams{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	err = SendToolGenAction(ctx, "exit", NewExitAction())
	if err != nil {
		return "", err
	}

	return params.FinalResult, nil
}

type transferToAgent struct{}

func (tta transferToAgent) Info(_ context.Context) (*schema.ToolInfo, error) {
	return toolInfoTransferToAgent, nil
}

func transferToAgentToolOutput(destName string) string {
	return fmt.Sprintf("successfully transferred to agent [%s]", destName)
}

func (tta transferToAgent) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type transferParams struct {
		AgentName string `json:"agent_name"`
	}

	params := &transferParams{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	err = SendToolGenAction(ctx, TransferToAgentToolName, NewTransferToAgentAction(params.AgentName))
	if err != nil {
		return "", err
	}

	return transferToAgentToolOutput(params.AgentName), nil
}

func (a *ChatModelAgent) Name(_ context.Context) string {
	return a.name
}

func (a *ChatModelAgent) Description(_ context.Context) string {
	return a.description
}

func (a *ChatModelAgent) OnSetSubAgents(_ context.Context, subAgents []Agent) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	if len(a.subAgents) > 0 {
		return errors.New("agent's sub-agents has already been set")
	}

	a.subAgents = subAgents
	return nil
}

func (a *ChatModelAgent) OnSetAsSubAgent(_ context.Context, parent Agent) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	if a.parentAgent != nil {
		return errors.New("agent has already been set as a sub-agent of another agent")
	}

	a.parentAgent = parent
	return nil
}

func (a *ChatModelAgent) OnDisallowTransferToParent(_ context.Context) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	a.disallowTransferToParent = true

	return nil
}

type cbHandler struct {
	*AsyncGenerator[*AgentEvent]
	agentName string

	enableStreaming bool
	store           *mockStore
}

func (h *cbHandler) onChatModelEnd(ctx context.Context,
	_ *callbacks.RunInfo, output *model.CallbackOutput) context.Context {

	event := EventFromMessage(output.Message, nil, schema.Assistant, "")
	h.Send(event)
	return ctx
}

func (h *cbHandler) onChatModelEndWithStreamOutput(ctx context.Context,
	_ *callbacks.RunInfo, output *schema.StreamReader[*model.CallbackOutput]) context.Context {

	cvt := func(in *model.CallbackOutput) (Message, error) {
		return in.Message, nil
	}
	out := schema.StreamReaderWithConvert(output, cvt)
	event := EventFromMessage(nil, out, schema.Assistant, "")
	h.Send(event)

	return ctx
}

func (h *cbHandler) onToolEnd(ctx context.Context,
	runInfo *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {

	toolCallID := compose.GetToolCallID(ctx)
	msg := schema.ToolMessage(output.Response, toolCallID, schema.WithToolName(runInfo.Name))
	event := EventFromMessage(msg, nil, schema.Tool, runInfo.Name)

	action := popToolGenAction(ctx, runInfo.Name)
	event.Action = action

	h.Send(event)

	return ctx
}

func (h *cbHandler) onToolEndWithStreamOutput(ctx context.Context,
	runInfo *callbacks.RunInfo, output *schema.StreamReader[*tool.CallbackOutput]) context.Context {

	toolCallID := compose.GetToolCallID(ctx)
	cvt := func(in *tool.CallbackOutput) (Message, error) {
		return schema.ToolMessage(in.Response, toolCallID), nil
	}
	out := schema.StreamReaderWithConvert(output, cvt)
	event := EventFromMessage(nil, out, schema.Tool, runInfo.Name)
	h.Send(event)

	return ctx
}

type tempInterruptInfo struct { // replace temp info by info when save the data
	info *compose.InterruptInfo
	data []byte
}

func (h *cbHandler) onGraphError(ctx context.Context,
	_ *callbacks.RunInfo, err error) context.Context {

	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		h.Send(&AgentEvent{Err: err})
		return ctx
	}

	data, existed, err := h.store.Get(ctx, mockCheckPointID)
	if err != nil {
		h.Send(&AgentEvent{AgentName: h.agentName, Err: fmt.Errorf("failed to get interrupt info: %w", err)})
		return ctx
	}
	if !existed {
		h.Send(&AgentEvent{AgentName: h.agentName, Err: fmt.Errorf("interrupt has happened, but cannot find interrupt info")})
		return ctx
	}
	h.Send(&AgentEvent{AgentName: h.agentName, Action: &AgentAction{
		Interrupted: &InterruptInfo{
			Data: &tempInterruptInfo{data: data, info: info},
		},
	}})

	return ctx
}

func genReactCallbacks(agentName string,
	generator *AsyncGenerator[*AgentEvent],
	enableStreaming bool,
	store *mockStore) compose.Option {

	h := &cbHandler{AsyncGenerator: generator, agentName: agentName, store: store, enableStreaming: enableStreaming}

	cmHandler := &ub.ModelCallbackHandler{
		OnEnd:                 h.onChatModelEnd,
		OnEndWithStreamOutput: h.onChatModelEndWithStreamOutput,
	}
	toolHandler := &ub.ToolCallbackHandler{
		OnEnd:                 h.onToolEnd,
		OnEndWithStreamOutput: h.onToolEndWithStreamOutput,
	}
	graphHandler := callbacks.NewHandlerBuilder().OnErrorFn(h.onGraphError).Build()

	cb := ub.NewHandlerHelper().ChatModel(cmHandler).Tool(toolHandler).Graph(graphHandler).Handler()

	return compose.WithCallbacks(cb)
}

func setOutputToSession(ctx context.Context, msg Message, msgStream MessageStream, outputKey string) error {
	if msg != nil {
		SetSessionValue(ctx, outputKey, msg.Content)
		return nil
	}

	concatenated, err := schema.ConcatMessageStream(msgStream)
	if err != nil {
		return err
	}

	SetSessionValue(ctx, outputKey, concatenated.Content)
	return nil
}

func errFunc(err error) runFunc {
	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *mockStore, _ ...compose.Option) {
		generator.Send(&AgentEvent{Err: err})
	}
}

func (a *ChatModelAgent) buildRunFunc(ctx context.Context) runFunc {
	a.once.Do(func() {
		instruction := a.instruction
		toolsNodeConf := a.toolsConfig.ToolsNodeConfig
		returnDirectly := copyMap(a.toolsConfig.ReturnDirectly)

		transferToAgents := a.subAgents
		if a.parentAgent != nil && !a.disallowTransferToParent {
			transferToAgents = append(transferToAgents, a.parentAgent)
		}

		if len(transferToAgents) > 0 {
			transferInstruction := genTransferToAgentInstruction(ctx, transferToAgents)
			instruction = concatInstructions(instruction, transferInstruction)

			toolsNodeConf.Tools = append(toolsNodeConf.Tools, &transferToAgent{})
			returnDirectly[TransferToAgentToolName] = true
		}

		if a.exit != nil {
			toolsNodeConf.Tools = append(toolsNodeConf.Tools, a.exit)
			exitInfo, err := a.exit.Info(ctx)
			if err != nil {
				a.run = errFunc(err)
				return
			}
			returnDirectly[exitInfo.Name] = true
		}

		if len(toolsNodeConf.Tools) == 0 {
			a.run = func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *mockStore, opts ...compose.Option) {
				var err error
				var msgs []Message
				msgs, err = a.genModelInput(ctx, instruction, input)
				if err != nil {
					generator.Send(&AgentEvent{Err: err})
					return
				}

				var msg Message
				var msgStream MessageStream
				if input.EnableStreaming {
					msgStream, err = a.model.Stream(ctx, msgs) // todo: chat model option
				} else {
					msg, err = a.model.Generate(ctx, msgs)
				}

				var event *AgentEvent
				if err == nil {
					if a.outputKey != "" {
						if msgStream != nil {
							// copy the stream first because when setting output to session, the stream will be consumed
							ss := msgStream.Copy(2)
							event = EventFromMessage(msg, ss[1], schema.Assistant, "")
							msgStream = ss[0]
						} else {
							event = EventFromMessage(msg, nil, schema.Assistant, "")
						}
						// send event asap, because setting output to session will block until stream fully consumed
						generator.Send(event)
						err = setOutputToSession(ctx, msg, msgStream, a.outputKey)
						if err != nil {
							generator.Send(&AgentEvent{Err: err})
						}
					} else {
						event = EventFromMessage(msg, msgStream, schema.Assistant, "")
						generator.Send(event)
					}
				} else {
					event = &AgentEvent{Err: err}
					generator.Send(event)
				}

				generator.Close()
			}

			return
		}

		// react
		conf := &reactConfig{
			model:               a.model,
			toolsConfig:         &toolsNodeConf,
			toolsReturnDirectly: returnDirectly,
			agentName:           a.name,
		}

		g, err := newReact(ctx, conf)
		if err != nil {
			a.run = errFunc(err)
			return
		}

		a.run = func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *mockStore, opts ...compose.Option) {
			var compileOptions []compose.GraphCompileOption
			compileOptions = append(compileOptions, compose.WithGraphName("React"), compose.WithCheckPointStore(store), compose.WithSerializer(&gobSerializer{}))
			if a.maxStep > 0 {
				compileOptions = append(compileOptions, compose.WithMaxRunSteps(a.maxStep))
			}
			runnable, err_ := g.Compile(ctx, compileOptions...)
			if err != nil {
				generator.Send(&AgentEvent{AgentName: a.name, Err: err})
				return
			}

			var msgs []Message
			msgs, err_ = a.genModelInput(ctx, instruction, input)
			if err_ != nil {
				generator.Send(&AgentEvent{Err: err_})
				return
			}

			callOpt := genReactCallbacks(a.name, generator, input.EnableStreaming, store)

			var msg Message
			var msgStream MessageStream
			if input.EnableStreaming {
				msgStream, err_ = runnable.Stream(ctx, msgs, append(opts, callOpt)...)
			} else {
				msg, err_ = runnable.Invoke(ctx, msgs, append(opts, callOpt)...)
			}

			if err_ == nil {
				if a.outputKey != "" {
					err_ = setOutputToSession(ctx, msg, msgStream, a.outputKey)
					if err_ != nil {
						generator.Send(&AgentEvent{Err: err_})
					}
				} else if msgStream != nil {
					msgStream.Close()
				}
			}

			generator.Close()
		}
	})

	atomic.StoreUint32(&a.frozen, 1)

	return a.run
}

func (a *ChatModelAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	run := a.buildRunFunc(ctx)

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(mockCheckPointID))

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			}

			generator.Close()
		}()

		run(ctx, input, generator, newEmptyStore(), co...)
	}()

	return iterator
}

func (a *ChatModelAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	run := a.buildRunFunc(ctx)

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(mockCheckPointID))

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			}

			generator.Close()
		}()

		run(ctx, &AgentInput{EnableStreaming: info.EnableStreaming}, generator, newResumeStore(info.Data.([]byte)), co...)
	}()

	return iterator
}

func getComposeOptions(opts []AgentRunOption) []compose.Option {
	o := GetImplSpecificOptions[chatModelAgentRunOptions](nil, opts...)
	var co []compose.Option
	if len(o.chatModelOptions) > 0 {
		co = append(co, compose.WithChatModelOption(o.chatModelOptions...))
	}
	var to []tool.Option
	if len(o.toolOptions) > 0 {
		to = append(to, o.toolOptions...)
	}
	for toolName, atos := range o.agentToolOptions {
		to = append(to, withAgentToolOptions(toolName, atos))
	}
	if len(to) > 0 {
		co = append(co, compose.WithToolsNodeOption(compose.WithToolOption(to...)))
	}
	if o.historyModifier != nil {
		co = append(co, compose.WithStateModifier(func(ctx context.Context, path compose.NodePath, state any) error {
			s, ok := state.(*State)
			if !ok {
				return fmt.Errorf("unexpected state type: %T, expected: %T", state, &State{})
			}
			s.Messages = o.historyModifier(ctx, s.Messages)
			return nil
		}))
	}
	return co
}

type gobSerializer struct{}

func (g *gobSerializer) Marshal(v any) ([]byte, error) {
	buf := new(bytes.Buffer)
	err := gob.NewEncoder(buf).Encode(v)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (g *gobSerializer) Unmarshal(data []byte, v any) error {
	buf := bytes.NewBuffer(data)
	return gob.NewDecoder(buf).Decode(v)
}
