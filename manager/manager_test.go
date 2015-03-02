// Copyright 2014 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Per-container manager.

package manager

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/cadvisor/container"
	"github.com/google/cadvisor/container/docker"
	"github.com/google/cadvisor/events"
	"github.com/google/cadvisor/info"
	itest "github.com/google/cadvisor/info/test"
	"github.com/google/cadvisor/storage/memory"
	"github.com/google/cadvisor/utils/sysfs/fakesysfs"
	"github.com/stretchr/testify/assert"
)

// TODO(vmarmol): Refactor these tests.

func createManagerAndAddContainers(
	memoryStorage *memory.InMemoryStorage,
	sysfs *fakesysfs.FakeSysFs,
	containers []string,
	f func(*container.MockContainerHandler),
	t *testing.T,
) *manager {
	container.ClearContainerHandlerFactories()
	mif, err := New(memoryStorage, sysfs)
	if err != nil {
		t.Fatal(err)
	}
	if ret, ok := mif.(*manager); ok {
		for _, name := range containers {
			mockHandler := container.NewMockContainerHandler(name)
			spec := itest.GenerateRandomContainerSpec(4)
			mockHandler.On("GetSpec").Return(
				spec,
				nil,
			).Once()
			cont, err := newContainerData(name, memoryStorage, mockHandler, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			ret.containers[namespacedContainerName{
				Name: name,
			}] = cont
			// Add Docker containers under their namespace.
			if strings.HasPrefix(name, "/docker") {
				ret.containers[namespacedContainerName{
					Namespace: docker.DockerNamespace,
					Name:      strings.TrimPrefix(name, "/docker/"),
				}] = cont
			}
			f(mockHandler)
		}
		return ret
	}
	t.Fatal("Wrong type")
	return nil
}

// Expect a manager with the specified containers and query. Returns the manager, map of ContainerInfo objects,
// and map of MockContainerHandler objects.}
func expectManagerWithContainers(containers []string, query *info.ContainerInfoRequest, t *testing.T) (*manager, map[string]*info.ContainerInfo, map[string]*container.MockContainerHandler) {
	infosMap := make(map[string]*info.ContainerInfo, len(containers))
	handlerMap := make(map[string]*container.MockContainerHandler, len(containers))

	for _, container := range containers {
		infosMap[container] = itest.GenerateRandomContainerInfo(container, 4, query, 1*time.Second)
	}

	memoryStorage := memory.New(query.NumStats, nil)
	sysfs := &fakesysfs.FakeSysFs{}
	m := createManagerAndAddContainers(
		memoryStorage,
		sysfs,
		containers,
		func(h *container.MockContainerHandler) {
			cinfo := infosMap[h.Name]
			ref, err := h.ContainerReference()
			if err != nil {
				t.Error(err)
			}
			for _, stat := range cinfo.Stats {
				err = memoryStorage.AddStats(ref, stat)
				if err != nil {
					t.Error(err)
				}
			}
			spec := cinfo.Spec

			h.On("ListContainers", container.ListSelf).Return(
				[]info.ContainerReference(nil),
				nil,
			)
			h.On("GetSpec").Return(
				spec,
				nil,
			)
			handlerMap[h.Name] = h
		},
		t,
	)

	return m, infosMap, handlerMap
}

func TestGetContainerInfo(t *testing.T) {
	containers := []string{
		"/c1",
		"/c2",
	}

	query := &info.ContainerInfoRequest{
		NumStats: 256,
	}

	m, infosMap, handlerMap := expectManagerWithContainers(containers, query, t)

	returnedInfos := make(map[string]*info.ContainerInfo, len(containers))

	for _, container := range containers {
		cinfo, err := m.GetContainerInfo(container, query)
		if err != nil {
			t.Fatalf("Unable to get info for container %v: %v", container, err)
		}
		returnedInfos[container] = cinfo
	}

	for container, handler := range handlerMap {
		handler.AssertExpectations(t)
		returned := returnedInfos[container]
		expected := infosMap[container]
		if !reflect.DeepEqual(returned, expected) {
			t.Errorf("returned unexpected info for container %v; returned %+v; expected %+v", container, returned, expected)
		}
	}

}

func TestAddDeleteContainersEventHandling(t *testing.T) {
	containers := []string{
		"/c1",
		"/c2",
	}

	query := &info.ContainerInfoRequest{
		NumStats: 256,
	}

	m, _, _ := expectManagerWithContainers(containers, query, t)

	request := events.NewRequest()
	request.EventType[events.TypeContainerDeletion] = true

	m.destroyContainer("/c2")
	eventsFound, err := m.eventHandler.GetEvents(request)
	assert.Nil(t, err)

	if eventsFound.Len() != 1 {
		t.Fatalf("Should have only found 1 destroyed container but found %v", eventsFound.Len())
	}
	if eventsFound[0].ContainerName != "/c2" {
		t.Errorf("Expected container name %v but got %v", "/c2", eventsFound[0].ContainerName)
	}
	if eventsFound[0].EventType != events.TypeContainerDeletion {
		t.Errorf("Expected deletion event type but got %v", eventsFound[0].EventType)
	}

	request.EventType[events.TypeContainerCreation] = true

	m.createContainer("/c3")
	eventsFound, err = m.eventHandler.GetEvents(request)
	assert.Nil(t, err)

	if eventsFound.Len() != 1 {
		t.Fatalf("Should have only found 1 container but found %v", eventsFound.Len())
	}
	if eventsFound[0].ContainerName != "/c3" {
		t.Errorf("Expected container name %v but got %v", "/c3", eventsFound[0].ContainerName)
	}
	if eventsFound[0].EventType != events.TypeContainerCreation {
		t.Errorf("Expected creation event type but got %v", eventsFound[0].EventType)
	}
}

func TestSubcontainersInfo(t *testing.T) {
	containers := []string{
		"/c1",
		"/c2",
	}

	query := &info.ContainerInfoRequest{
		NumStats: 64,
	}

	m, infosMap, handlerMap := expectManagerWithContainers(containers, query, t)

	returnedInfos := make(map[string]*info.ContainerInfo, len(containers))

	for _, container := range containers {
		cinfo, err := m.GetContainerInfo(container, query)
		if err != nil {
			t.Fatalf("Unable to get info for container %v: %v", container, err)
		}
		returnedInfos[container] = cinfo
	}

	for container, handler := range handlerMap {
		handler.AssertExpectations(t)
		returned := returnedInfos[container]
		expected := infosMap[container]
		if !reflect.DeepEqual(returned, expected) {
			t.Errorf("returned unexpected info for container %v; returned %+v; expected %+v", container, returned, expected)
		}
	}

}

func TestDockerContainersInfo(t *testing.T) {
	containers := []string{
		"/docker/c1",
	}

	query := &info.ContainerInfoRequest{
		NumStats: 2,
	}

	m, _, _ := expectManagerWithContainers(containers, query, t)

	result, err := m.DockerContainer("c1", query)
	if err != nil {
		t.Fatalf("expected to succeed: %s", err)
	}
	if result.Name != containers[0] {
		t.Errorf("Unexpected container %q in result. Expected container %q", result.Name, containers[0])
	}
}

func TestNew(t *testing.T) {
	memoryStorage := memory.New(60, nil)
	manager, err := New(memoryStorage, &fakesysfs.FakeSysFs{})
	if err != nil {
		t.Fatalf("Expected manager.New to succeed: %s", err)
	}
	if manager == nil {
		t.Fatalf("Expected returned manager to not be nil")
	}
}

func TestNewNilManager(t *testing.T) {
	_, err := New(nil, nil)
	if err == nil {
		t.Fatalf("Expected nil manager to return error")
	}
}
