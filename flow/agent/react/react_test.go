/*
 * Copyright 2024 CloudWeGo Authors
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

package react

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
	template "github.com/cloudwego/eino/utils/callbacks"
)

func TestReact(t *testing.T) {
	ctx := context.Background()

	fakeTool := &fakeToolGreetForTest{
		tarCount: 3,
	}

	info, err := fakeTool.Info(ctx)
	assert.NoError(t, err)

	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockChatModel(ctrl)

	times := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			times++
			if times <= 2 {
				info, _ := fakeTool.Info(ctx)

				return schema.AssistantMessage("hello max",
						[]schema.ToolCall{
							{
								ID: randStr(),
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "123"}`, randStr()),
								},
							},
						}),
					nil
			}

			return schema.AssistantMessage("bye", nil), nil
		}).AnyTimes()
	cm.EXPECT().BindTools(gomock.Any()).Return(nil).AnyTimes()

	err = cm.BindTools([]*schema.ToolInfo{info})
	assert.NoError(t, err)

	a, err := NewAgent(ctx, &AgentConfig{
		Model: cm,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{fakeTool},
		},
		MessageModifier: func(ctx context.Context, input []*schema.Message) []*schema.Message {
			assert.Equal(t, len(input), times*2+1)
			return input
		},
		MaxStep: 40,
	})
	assert.Nil(t, err)

	out, err := a.Generate(ctx, []*schema.Message{
		{
			Role:    schema.User,
			Content: "Use greet tool to continuously say hello until you get a bye response, greet names in the following order: max, bob, alice, john, marry, joe, ken, lily, please start directly! please start directly! please start directly!",
		},
	}, agent.WithComposeOptions(compose.WithCallbacks(callbackForTest)))
	assert.Nil(t, err)

	if out != nil {
		t.Log(out.Content)
	}

	// test return directly
	times = 0
	a, err = NewAgent(ctx, &AgentConfig{
		Model: cm,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{fakeTool},
		},
		MessageModifier: func(ctx context.Context, input []*schema.Message) []*schema.Message {
			assert.Equal(t, len(input), times*2+1)
			return input
		},
		MaxStep:            40,
		ToolReturnDirectly: map[string]struct{}{info.Name: {}},
	})
	assert.Nil(t, err)

	out, err = a.Generate(ctx, []*schema.Message{
		{
			Role:    schema.User,
			Content: "Use greet tool to continuously say hello until you get a bye response, greet names in the following order: max, bob, alice, john, marry, joe, ken, lily, please start directly! please start directly! please start directly!",
		},
	}, agent.WithComposeOptions(compose.WithCallbacks(callbackForTest)))
	assert.Nil(t, err)

	if out != nil {
		t.Log(out.Content)
	}
}

func TestReactStream(t *testing.T) {
	ctx := context.Background()

	fakeTool := &fakeToolGreetForTest{
		tarCount: 20,
	}

	fakeStreamTool := &fakeStreamToolGreetForTest{
		tarCount: 20,
	}

	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockChatModel(ctrl)

	times := 0
	cm.EXPECT().BindTools(gomock.Any()).Return(nil).AnyTimes()
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (
			*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			defer sw.Close()

			info, _ := fakeTool.Info(ctx)
			streamInfo, _ := fakeStreamTool.Info(ctx)

			times++
			if times <= 2 {
				sw.Send(schema.AssistantMessage("hello max",
					[]schema.ToolCall{
						{
							ID: randStr(),
							Function: schema.FunctionCall{
								Name:      info.Name,
								Arguments: fmt.Sprintf(`{"name": "%s", "hh": "tool"}`, randStr()),
							},
						},
					}),
					nil)
				return sr, nil
			} else if times == 3 {
				sw.Send(schema.AssistantMessage("hello max",
					[]schema.ToolCall{
						{
							ID: randStr(),
							Function: schema.FunctionCall{
								Name:      streamInfo.Name,
								Arguments: fmt.Sprintf(`{"name": "%s", "hh": "stream tool"}`, randStr()),
							},
						},
					}),
					nil)
				return sr, nil
			} else if times == 4 { // parallel tool call
				sw.Send(schema.AssistantMessage("hello max",
					[]schema.ToolCall{
						{
							ID: randStr(),
							Function: schema.FunctionCall{
								Name:      info.Name,
								Arguments: fmt.Sprintf(`{"name": "%s", "hh": "tool"}`, randStr()),
							},
						},
						{
							ID: randStr(),
							Function: schema.FunctionCall{
								Name:      streamInfo.Name,
								Arguments: fmt.Sprintf(`{"name": "%s", "hh": "stream tool"}`, randStr()),
							},
						},
					}),
					nil)
				return sr, nil
			}

			sw.Send(schema.AssistantMessage("bye", nil), nil)
			return sr, nil
		}).AnyTimes()

	a, err := NewAgent(ctx, &AgentConfig{
		Model: cm,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{fakeTool, fakeStreamTool},
		},

		MaxStep: 40,
	})
	assert.Nil(t, err)

	out, err := a.Stream(ctx, []*schema.Message{
		{
			Role:    schema.User,
			Content: "Use greet tool to continuously say hello until you get a bye response, greet names in the following order: max, bob, alice, john, marry, joe, ken, lily, please start directly! please start directly! please start directly!",
		},
	}, agent.WithComposeOptions(compose.WithCallbacks(callbackForTest)))
	if err != nil {
		t.Fatal(err)
	}

	defer out.Close()

	msgs := make([]*schema.Message, 0)
	for {
		msg, err := out.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			t.Fatal(err)
		}

		msgs = append(msgs, msg)
	}

	assert.Equal(t, 1, len(msgs))

	msg, err := schema.ConcatMessages(msgs)
	if err != nil {
		t.Fatal(err)
	}

	t.Log(msg.Content)

	info, err := fakeStreamTool.Info(ctx)
	assert.NoError(t, err)

	// test return directly
	a, err = NewAgent(ctx, &AgentConfig{
		Model: cm,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{fakeTool, fakeStreamTool},
		},

		MaxStep:            40,
		ToolReturnDirectly: map[string]struct{}{info.Name: {}}, // one of the two tools is return directly
	})
	assert.Nil(t, err)

	times = 0
	out, err = a.Stream(ctx, []*schema.Message{
		{
			Role:    schema.User,
			Content: "Use greet tool to continuously say hello until you get a bye response, greet names in the following order: max, bob, alice, john, marry, joe, ken, lily, please start directly! please start directly! please start directly!",
		},
	}, agent.WithComposeOptions(compose.WithCallbacks(callbackForTest)))
	if err != nil {
		t.Fatal(err)
	}

	defer out.Close()

	msgs = make([]*schema.Message, 0)
	for {
		msg, err := out.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			t.Fatal(err)
		}

		msgs = append(msgs, msg)
	}

	assert.Equal(t, 1, len(msgs))

	msg, err = schema.ConcatMessages(msgs)
	if err != nil {
		t.Fatal(err)
	}

	t.Log(msg.Content)

	// return directly tool call within parallel tool calls
	out, err = a.Stream(ctx, []*schema.Message{
		{
			Role:    schema.User,
			Content: "Use greet tool to continuously say hello until you get a bye response, greet names in the following order: max, bob, alice, john, marry, joe, ken, lily, please start directly! please start directly! please start directly!",
		},
	}, agent.WithComposeOptions(compose.WithCallbacks(callbackForTest)))
	assert.NoError(t, err)

	defer out.Close()

	msgs = make([]*schema.Message, 0)
	for {
		msg, err := out.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			assert.NoError(t, err)
		}

		msgs = append(msgs, msg)
	}

	assert.Equal(t, 1, len(msgs))

	msg, err = schema.ConcatMessages(msgs)
	assert.NoError(t, err)

	t.Log("parallel tool call with return directly: ", msg.Content)
}

func TestReactWithModifier(t *testing.T) {
	ctx := context.Background()

	fakeTool := &fakeToolGreetForTest{}
	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockChatModel(ctrl)

	times := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			times++
			if times <= 2 {
				info, _ := fakeTool.Info(ctx)

				return schema.AssistantMessage("hello max",
						[]schema.ToolCall{
							{
								ID: randStr(),
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "123"}`, randStr()),
								},
							},
						}),
					nil
			}

			return schema.AssistantMessage("bye", nil), nil
		}).AnyTimes()
	cm.EXPECT().BindTools(gomock.Any()).Return(nil).AnyTimes()

	a, err := NewAgent(ctx, &AgentConfig{
		Model: cm,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: []tool.BaseTool{fakeTool},
		},
		MessageModifier: func(ctx context.Context, input []*schema.Message) []*schema.Message {
			res := make([]*schema.Message, 0, len(input)+1)

			res = append(res, schema.SystemMessage("you are a helpful assistant"))
			res = append(res, input...)
			return res
		},

		MaxStep: 40,
	})

	assert.Nil(t, err)

	out, err := a.Generate(ctx, []*schema.Message{
		{
			Role:    schema.User,
			Content: "hello",
		},
	}, agent.WithComposeOptions(compose.WithCallbacks(callbackForTest)))
	if err != nil {
		t.Fatal(err)
	}

	if out != nil {
		t.Log(out.Content)
	}
}

func TestAgentInGraph(t *testing.T) {
	t.Run("agent generate in chain", func(t *testing.T) {
		ctx := context.Background()

		fakeTool := &fakeToolGreetForTest{}
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockChatModel(ctrl)

		times := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {

				times += 1
				if times <= 2 {
					info, _ := fakeTool.Info(ctx)

					return schema.AssistantMessage("hello max",
							[]schema.ToolCall{
								{
									ID: randStr(),
									Function: schema.FunctionCall{
										Name:      info.Name,
										Arguments: fmt.Sprintf(`{"name": "%s", "hh": "123"}`, randStr()),
									},
								},
							}),
						nil
				}

				return schema.AssistantMessage("bye", nil), nil

			}).Times(3)
		cm.EXPECT().BindTools(gomock.Any()).Return(nil).AnyTimes()

		a, err := NewAgent(ctx, &AgentConfig{
			Model: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool, &fakeStreamToolGreetForTest{}},
			},

			MaxStep: 40,
		})
		assert.Nil(t, err)

		chain := compose.NewChain[[]*schema.Message, string]()
		agentLambda, err := compose.AnyLambda(a.Generate, a.Stream, nil, nil)
		assert.Nil(t, err)

		chain.
			AppendLambda(agentLambda).
			AppendLambda(compose.InvokableLambda(func(ctx context.Context, input *schema.Message) (string, error) {
				t.Log("got agent response: ", input.Content)
				return input.Content, nil
			}))
		r, err := chain.Compile(ctx)
		assert.Nil(t, err)

		res, err := r.Invoke(ctx, []*schema.Message{{Role: schema.User, Content: "hello"}},
			compose.WithCallbacks(callbackForTest))
		assert.Nil(t, err)

		t.Log(res)
	})

	t.Run("agent stream in chain", func(t *testing.T) {

		fakeStreamTool := &fakeStreamToolGreetForTest{}
		ctx := context.Background()
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockChatModel(ctrl)

		times := 0
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (
				*schema.StreamReader[*schema.Message], error) {
				sr, sw := schema.Pipe[*schema.Message](1)
				defer sw.Close()

				times += 1
				if times <= 2 {
					info, _ := fakeStreamTool.Info(ctx)
					sw.Send(schema.AssistantMessage("hello max",
						[]schema.ToolCall{
							{
								ID: randStr(),
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "123"}`, randStr()),
								},
							},
						}),
						nil)
					return sr, nil
				}

				sw.Send(schema.AssistantMessage("bye", nil), nil)
				return sr, nil
			}).Times(3)
		cm.EXPECT().BindTools(gomock.Any()).Return(nil).AnyTimes()

		a, err := NewAgent(ctx, &AgentConfig{
			Model: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&fakeToolGreetForTest{}, fakeStreamTool},
			},

			MaxStep: 40,
		})
		assert.Nil(t, err)

		chain := compose.NewChain[[]*schema.Message, string]()
		agentGraph, opts := a.ExportGraph()
		assert.Nil(t, err)

		chain.
			AppendGraph(agentGraph, opts...).
			AppendLambda(compose.InvokableLambda(func(ctx context.Context, input *schema.Message) (string, error) {
				t.Log("got agent response: ", input.Content)
				return input.Content, nil
			}))
		r, err := chain.Compile(ctx)
		assert.Nil(t, err)

		outStream, err := r.Stream(ctx, []*schema.Message{{Role: schema.User, Content: "hello"}},
			compose.WithCallbacks(callbackForTest))
		if err != nil {
			t.Fatal(err)
		}

		defer outStream.Close()

		msg := ""
		for {
			msgItem, err := outStream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}

				t.Fatal(err)
			}

			msg += msgItem
		}

		t.Log(msg)
	})

}

func TestReActAgentWithNoTools(t *testing.T) {
	// create the react agent with no tools, assert no error
	// then invoke the agent with two options: WithToolList, WithChatModelOptions(model.WithTools),
	// to dynamically add tools to the agent, assert the tool is successfully called.
	fakeTool := &fakeToolGreetForTest{}
	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	times := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {

			times += 1
			if times <= 2 {
				info, _ := fakeTool.Info(ctx)

				return schema.AssistantMessage("hello max",
						[]schema.ToolCall{
							{
								ID: randStr(),
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "123"}`, randStr()),
								},
							},
						}),
					nil
			}

			return schema.AssistantMessage("bye", nil), nil

		}).Times(3)

	ra, err := NewAgent(context.Background(), &AgentConfig{
		ToolCallingModel: cm,
		MaxStep:          10,
	})
	assert.NoError(t, err)

	info, _ := fakeTool.Info(context.Background())
	msg, err := ra.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("hello"),
	}, WithToolList(fakeTool), WithChatModelOptions(model.WithTools([]*schema.ToolInfo{info})))
	assert.NoError(t, err)
	assert.Equal(t, "bye", msg.Content)
}

type fakeStreamToolGreetForTest struct {
	tarCount int
	curCount int
}

func (t *fakeStreamToolGreetForTest) StreamableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (
	*schema.StreamReader[string], error) {
	p := &fakeToolInput{}
	err := sonic.UnmarshalString(argumentsInJSON, p)
	if err != nil {
		return nil, err
	}

	if t.curCount >= t.tarCount {
		s := schema.StreamReaderFromArray([]string{`{"say": "bye"}`})
		return s, nil
	}
	t.curCount++
	s := schema.StreamReaderFromArray([]string{fmt.Sprintf(`{"say": "hello %v"}`, p.Name)})
	return s, nil
}

type fakeToolGreetForTest struct {
	tarCount int
	curCount int
}

func (t *fakeToolGreetForTest) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "greet",
		Desc: "greet with name",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Desc:     "user name who to greet",
					Required: true,
					Type:     schema.String,
				},
			}),
	}, nil
}

func (t *fakeStreamToolGreetForTest) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "greet in stream",
		Desc: "greet with name in stream",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Desc:     "user name who to greet",
					Required: true,
					Type:     schema.String,
				},
			}),
	}, nil
}

func (t *fakeToolGreetForTest) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	p := &fakeToolInput{}
	err := sonic.UnmarshalString(argumentsInJSON, p)
	if err != nil {
		return "", err
	}

	if t.curCount >= t.tarCount {
		return `{"say": "bye"}`, nil
	}

	t.curCount++
	return fmt.Sprintf(`{"say": "hello %v"}`, p.Name), nil
}

type fakeToolInput struct {
	Name string `json:"name"`
}

func randStr() string {
	seeds := []rune("this is a seed")
	b := make([]rune, 8)
	for i := range b {
		b[i] = seeds[rand.Intn(len(seeds))]
	}
	return string(b)
}

var callbackForTest = BuildAgentCallback(&template.ModelCallbackHandler{}, &template.ToolCallbackHandler{})
