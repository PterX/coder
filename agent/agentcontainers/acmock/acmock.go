// Code generated by MockGen. DO NOT EDIT.
// Source: .. (interfaces: Lister,DevcontainerCLI)
//
// Generated by this command:
//
//	mockgen -destination ./acmock.go -package acmock .. Lister,DevcontainerCLI
//

// Package acmock is a generated GoMock package.
package acmock

import (
	context "context"
	reflect "reflect"

	agentcontainers "github.com/coder/coder/v2/agent/agentcontainers"
	codersdk "github.com/coder/coder/v2/codersdk"
	gomock "go.uber.org/mock/gomock"
)

// MockLister is a mock of Lister interface.
type MockLister struct {
	ctrl     *gomock.Controller
	recorder *MockListerMockRecorder
	isgomock struct{}
}

// MockListerMockRecorder is the mock recorder for MockLister.
type MockListerMockRecorder struct {
	mock *MockLister
}

// NewMockLister creates a new mock instance.
func NewMockLister(ctrl *gomock.Controller) *MockLister {
	mock := &MockLister{ctrl: ctrl}
	mock.recorder = &MockListerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockLister) EXPECT() *MockListerMockRecorder {
	return m.recorder
}

// List mocks base method.
func (m *MockLister) List(ctx context.Context) (codersdk.WorkspaceAgentListContainersResponse, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", ctx)
	ret0, _ := ret[0].(codersdk.WorkspaceAgentListContainersResponse)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List.
func (mr *MockListerMockRecorder) List(ctx any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*MockLister)(nil).List), ctx)
}

// MockDevcontainerCLI is a mock of DevcontainerCLI interface.
type MockDevcontainerCLI struct {
	ctrl     *gomock.Controller
	recorder *MockDevcontainerCLIMockRecorder
	isgomock struct{}
}

// MockDevcontainerCLIMockRecorder is the mock recorder for MockDevcontainerCLI.
type MockDevcontainerCLIMockRecorder struct {
	mock *MockDevcontainerCLI
}

// NewMockDevcontainerCLI creates a new mock instance.
func NewMockDevcontainerCLI(ctrl *gomock.Controller) *MockDevcontainerCLI {
	mock := &MockDevcontainerCLI{ctrl: ctrl}
	mock.recorder = &MockDevcontainerCLIMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockDevcontainerCLI) EXPECT() *MockDevcontainerCLIMockRecorder {
	return m.recorder
}

// Up mocks base method.
func (m *MockDevcontainerCLI) Up(ctx context.Context, workspaceFolder, configPath string, opts ...agentcontainers.DevcontainerCLIUpOptions) (string, error) {
	m.ctrl.T.Helper()
	varargs := []any{ctx, workspaceFolder, configPath}
	for _, a := range opts {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "Up", varargs...)
	ret0, _ := ret[0].(string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Up indicates an expected call of Up.
func (mr *MockDevcontainerCLIMockRecorder) Up(ctx, workspaceFolder, configPath any, opts ...any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]any{ctx, workspaceFolder, configPath}, opts...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Up", reflect.TypeOf((*MockDevcontainerCLI)(nil).Up), varargs...)
}
