package registry

import (
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/moleculer-go/moleculer"
	"github.com/moleculer-go/moleculer/payload"
	"github.com/moleculer-go/moleculer/service"
	"github.com/moleculer-go/moleculer/strategy"
	log "github.com/sirupsen/logrus"
)

type ActionEntry struct {
	targetNodeID string
	action       *service.Action
	isLocal      bool
	service      *service.Service
	logger       *log.Entry
}

type actionsMap map[string][]ActionEntry

type ActionCatalog struct {
	actions sync.Map
	logger  *log.Entry
}

func CreateActionCatalog(logger *log.Entry) *ActionCatalog {
	return &ActionCatalog{actions: sync.Map{}, logger: logger}
}

var actionCallRecovery = true //TODO extract this to a Config - useful to turn for Debug in tests.

// catchActionError is called defered after invoking a local action
// if there is an error (recover () != nil) this functions log the error and stack track and encapsulate
// the error inside a moleculer.Payload
func (actionEntry *ActionEntry) catchActionError(context moleculer.BrokerContext, result chan moleculer.Payload) {
	if !actionCallRecovery {
		return
	}
	if err := recover(); err != nil {
		actionEntry.logger.Error("Action failed: ", context.ActionName(), "\n[Error]: ", err, "\n[Stack Trace]: ", string(debug.Stack()))
		errT, isError := err.(error)
		if isError {
			result <- payload.New(errT)
		} else {
			result <- payload.New(errors.New(fmt.Sprint(err)))
		}
	}
}

func (actionEntry *ActionEntry) invokeLocalAction(context moleculer.BrokerContext) chan moleculer.Payload {
	result := make(chan moleculer.Payload, 1)

	actionEntry.logger.Trace("Before Invoking action: ", context.ActionName(), " params: ", context.Payload())

	go func() {
		defer actionEntry.catchActionError(context, result)
		handler := actionEntry.action.Handler()
		actionResult := handler(context.(moleculer.Context), context.Payload())

		actionEntry.logger.Trace("After Invoking action: ", context.ActionName(), " result: ", actionResult)
		result <- payload.New(actionResult)
	}()

	return result
}

func (actionEntry ActionEntry) TargetNodeID() string {
	return actionEntry.targetNodeID
}

func (actionEntry ActionEntry) IsLocal() bool {
	return actionEntry.isLocal
}

func (actionEntry ActionEntry) Service() *service.Service {
	return actionEntry.service
}

func (actionCatalog *ActionCatalog) listByName() map[string][]ActionEntry {
	result := make(map[string][]ActionEntry)
	actionCatalog.actions.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.([]ActionEntry)
		return true
	})
	return result
}

func (actionCatalog *ActionCatalog) Find(name string, local bool) *ActionEntry {
	list, exists := actionCatalog.actions.Load(name)
	if !exists {
		return nil
	}
	actions := list.([]ActionEntry)
	if !local && len(actions) > 0 {
		return &actions[0]
	}
	for _, action := range actions {
		if action.isLocal {
			return &action
		}
	}
	return nil
}

// Add a new action to the catalog.
func (actionCatalog *ActionCatalog) Add(action service.Action, service *service.Service, local bool) {
	entry := ActionEntry{service.NodeID(), &action, local, service, actionCatalog.logger}
	name := action.FullName()
	list, exists := actionCatalog.actions.Load(name)
	if !exists {
		list = []ActionEntry{entry}
	} else {
		list = append(list.([]ActionEntry), entry)
	}
	actionCatalog.actions.Store(name, list)
}

func (actionCatalog *ActionCatalog) Update(nodeID string, fullname string, updates map[string]interface{}) {
	//TODO .. the only thing that can be udpated is the Action Schema (validation) and that does not exist yet
}

// RemoveByNode remove actions for the given nodeID.
func (actionCatalog *ActionCatalog) RemoveByNode(nodeID string) {
	actionCatalog.actions.Range(func(key, value interface{}) bool {
		name := key.(string)
		actions := value.([]ActionEntry)
		var toKeep []ActionEntry
		for _, action := range actions {
			if action.targetNodeID != nodeID {
				toKeep = append(toKeep, action)
			}
		}
		actionCatalog.actions.Store(name, toKeep)
		return true
	})
}

func (actionCatalog *ActionCatalog) Remove(nodeID string, name string) {
	value, exists := actionCatalog.actions.Load(name)
	if !exists {
		return
	}
	actions := value.([]ActionEntry)
	var toKeep []ActionEntry
	for _, action := range actions {
		if action.targetNodeID != nodeID {
			toKeep = append(toKeep, action)
		}
	}
	actionCatalog.actions.Store(name, toKeep)
}

func (actionCatalog *ActionCatalog) NextFromNode(actionName string, nodeID string) *ActionEntry {
	list, exists := actionCatalog.actions.Load(actionName)
	if !exists {
		return nil
	}
	actions := list.([]ActionEntry)
	for _, action := range actions {
		if action.targetNodeID == nodeID {
			return &action
		}
	}
	return nil
}

// Next find all actions registered in this node and use the strategy to select and return the best one to be called.
func (actionCatalog *ActionCatalog) Next(actionName string, stg strategy.Strategy) *ActionEntry {
	list, exists := actionCatalog.actions.Load(actionName)
	if !exists {
		actionCatalog.logger.Debug("actionCatalog.Next() no entries found for name: ", actionName, "  actionCatalog.actions: ", actionCatalog.actions)
		return nil
	}
	actions := list.([]ActionEntry)
	nodes := make([]strategy.Selector, len(actions))
	for index, action := range actions {
		nodes[index] = action
		if action.IsLocal() {
			return &action
		}
	}
	if selected := stg.Select(nodes); selected != nil {
		entry := (*selected).(ActionEntry)
		return &entry
	}
	actionCatalog.logger.Debug("actionCatalog.Next() no entries selected for name: ", actionName, "  actionCatalog.actions: ", actionCatalog.actions)
	return nil
}
