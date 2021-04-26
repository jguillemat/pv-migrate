package engine

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/utkuozdemir/pv-migrate/internal/job"
	"github.com/utkuozdemir/pv-migrate/internal/request"
	"github.com/utkuozdemir/pv-migrate/internal/strategy"
	"github.com/utkuozdemir/pv-migrate/internal/task"
	"github.com/utkuozdemir/pv-migrate/internal/testutil"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"testing"
)

func TestNewEngineEmptyStrategies(t *testing.T) {
	_, err := New([]strategy.Strategy{})
	if err == nil {
		t.Fatal("expected error for empty list of strategies")
	}
}

func TestNewEngineDuplicateStrategies(t *testing.T) {
	strategy1 := testutil.Strategy{
		NameVal: "strategy1",
	}
	strategy2 := testutil.Strategy{
		NameVal: "strategy1",
	}
	strategies := []strategy.Strategy{&strategy1, &strategy2}
	_, err := New(strategies)
	if err == nil {
		t.Fatal("expected error for duplicate strategies")
	}
}

func TestValidateRequestWithNonExistingStrategy(t *testing.T) {
	eng := testEngine(testStrategies()...)
	req := request.NewWithDefaultImages(nil, nil, request.NewOptionsWithDefaults(true), []string{"strategy3"})
	err := eng.validate(req)
	if err == nil {
		t.Fatal("expected error for non existing strategy")
	}
}

func TestBuildJob(t *testing.T) {
	testEngine := testEngine(testStrategies()...)
	testRequest := testRequest()
	migrationJob, err := testEngine.BuildJob(testRequest)
	migrationTask := task.New(migrationJob)
	assert.Nil(t, err)

	assert.Len(t, migrationTask.ID(), 5)

	assert.True(t, migrationJob.Options().DeleteExtraneousFiles())
	assert.Equal(t, "namespace1", migrationJob.Source().Claim().Namespace)
	assert.Equal(t, "pvc1", migrationJob.Source().Claim().Name)
	assert.Equal(t, "node1", migrationJob.Source().MountedNode())
	assert.False(t, migrationJob.Source().SupportsRWO())
	assert.True(t, migrationJob.Source().SupportsROX())
	assert.False(t, migrationJob.Source().SupportsRWX())
	assert.Equal(t, "namespace2", migrationJob.Dest().Claim().Namespace)
	assert.Equal(t, "pvc2", migrationJob.Dest().Claim().Name)
	assert.Equal(t, "node2", migrationJob.Dest().MountedNode())
	assert.True(t, migrationJob.Dest().SupportsRWO())
	assert.False(t, migrationJob.Dest().SupportsROX())
	assert.True(t, migrationJob.Dest().SupportsRWX())
}

func TestBuildJobMounted(t *testing.T) {
	testEngine := testEngine(testStrategies()...)
	testRequest := testRequestWithOptions(request.NewOptions(true, false))
	j, err := testEngine.BuildJob(testRequest)
	assert.Nil(t, j)
	assert.Error(t, err)
}

func TestFindStrategies(t *testing.T) {
	mockEngine := testEngine(testStrategies()...)
	allStrategies, _ := mockEngine.findStrategies("strategy1", "strategy2")
	assert.Len(t, allStrategies, 2)
	singleStrategy, _ := mockEngine.findStrategies("strategy1")
	assert.Len(t, singleStrategy, 1)
	assert.Equal(t, singleStrategy[0].Name(), "strategy1")
	emptyStrategies, _ := mockEngine.findStrategies()
	assert.Empty(t, emptyStrategies)
}

func TestDetermineStrategies(t *testing.T) {
	engine := testEngine(testStrategies()...)
	r := testRequest()
	testTask, _ := engine.BuildJob(r)
	strategies, _ := engine.determineStrategies(r, testTask)
	assert.Len(t, strategies, 2)
}

func TestDetermineStrategiesCorrectOrder(t *testing.T) {
	strategy1 := testutil.Strategy{
		NameVal:     "strategy1",
		CanDoVal:    canDoTrue,
		PriorityVal: 3000,
	}
	strategy2 := testutil.Strategy{
		NameVal:     "strategy2",
		CanDoVal:    canDoTrue,
		PriorityVal: 1000,
	}
	strategy3 := testutil.Strategy{
		NameVal:     "strategy3",
		CanDoVal:    canDoTrue,
		PriorityVal: 2000,
	}

	engine := testEngine(&strategy1, &strategy2, &strategy3)
	req := testRequest()
	testTask, _ := engine.BuildJob(req)
	strategies, _ := engine.determineStrategies(req, testTask)
	assert.Len(t, strategies, 3)
	assert.Equal(t, "strategy2", strategies[0].Name())
	assert.Equal(t, "strategy3", strategies[1].Name())
	assert.Equal(t, "strategy1", strategies[2].Name())
}

func TestDetermineStrategiesCannotDo(t *testing.T) {
	strategy1 := testutil.Strategy{
		NameVal:     "strategy1",
		CanDoVal:    canDoFalse,
		PriorityVal: 3000,
	}
	strategy2 := testutil.Strategy{
		NameVal:     "strategy2",
		CanDoVal:    canDoTrue,
		PriorityVal: 1000,
	}

	engine := testEngine(&strategy1, &strategy2)
	req := testRequest()
	testTask, _ := engine.BuildJob(req)
	strategies, _ := engine.determineStrategies(req, testTask)
	assert.Len(t, strategies, 1)
	assert.Equal(t, "strategy2", strategies[0].Name())
}

func TestDetermineStrategiesRequested(t *testing.T) {
	strategy1 := testutil.Strategy{
		NameVal:     "strategy1",
		CanDoVal:    canDoTrue,
		PriorityVal: 3000,
	}
	strategy2 := testutil.Strategy{
		NameVal:     "strategy2",
		CanDoVal:    canDoTrue,
		PriorityVal: 1000,
	}
	strategy3 := testutil.Strategy{
		NameVal:     "strategy3",
		CanDoVal:    canDoTrue,
		PriorityVal: 2000,
	}

	engine := testEngine(&strategy1, &strategy2, &strategy3)
	req := testRequest("strategy1", "strategy3")
	testTask, _ := engine.BuildJob(req)
	strategies, _ := engine.determineStrategies(req, testTask)
	assert.Len(t, strategies, 2)
	assert.Equal(t, "strategy1", strategies[0].Name())
	assert.Equal(t, "strategy3", strategies[1].Name())
}

func TestDetermineStrategiesRequestedNonExistent(t *testing.T) {
	strategy1 := testutil.Strategy{
		NameVal:     "strategy1",
		CanDoVal:    canDoTrue,
		PriorityVal: 3000,
	}

	engine := testEngine(&strategy1)
	req := testRequest("strategy1", "strategy2")
	testTask, _ := engine.BuildJob(req)
	strategies, err := engine.determineStrategies(req, testTask)
	assert.Nil(t, strategies)
	assert.NotNil(t, err)
}

func TestRun(t *testing.T) {
	var called []string
	cleanup := func(t task.Task) error {
		return nil
	}
	strategy1 := testutil.Strategy{
		NameVal:     "strategy1",
		CanDoVal:    canDoTrue,
		PriorityVal: 3000,
		RunFunc: func(t task.Task) error {
			called = append(called, "strategy1")
			return nil
		},
		CleanupFunc: cleanup,
	}
	strategy2 := testutil.Strategy{
		NameVal:     "strategy2",
		CanDoVal:    canDoTrue,
		PriorityVal: 1000,
		RunFunc: func(t task.Task) error {
			called = append(called, "strategy2")
			return errors.New("test error")
		},
		CleanupFunc: cleanup,
	}
	strategy3 := testutil.Strategy{
		NameVal:     "strategy3",
		CanDoVal:    canDoFalse,
		PriorityVal: 2000,
		RunFunc: func(t task.Task) error {
			called = append(called, "strategy3")
			return nil
		},
		CleanupFunc: cleanup,
	}

	engine := testEngine(&strategy1, &strategy2, &strategy3)
	req := testRequest()
	err := engine.Run(req)
	assert.Nil(t, err)
	assert.Len(t, called, 2)
	assert.Equal(t, "strategy2", called[0])
	assert.Equal(t, "strategy1", called[1])
}

func testRequest(strategies ...string) request.Request {
	options := request.NewOptions(true, true)
	return testRequestWithOptions(options, strategies...)
}

func testRequestWithOptions(options request.Options, strategies ...string) request.Request {
	source := request.NewPVC("/kubeconfig1", "context1", "namespace1", "pvc1")
	dest := request.NewPVC("/kubeconfig2", "context2", "namespace2", "pvc2")
	newRequest := request.NewWithDefaultImages(source, dest, options, strategies)
	return newRequest
}

func testEngine(strategies ...strategy.Strategy) Engine {
	pvcA := testutil.PVCWithAccessModes("namespace1", "pvc1", v1.ReadOnlyMany)
	pvcB := testutil.PVCWithAccessModes("namespace2", "pvc2", v1.ReadWriteOnce, v1.ReadWriteMany)
	podA := testutil.Pod("namespace1", "pod1", "node1", "pvc1")
	podB := testutil.Pod("namespace2", "pod2", "node2", "pvc2")
	kubernetesClientProvider := testutil.KubernetesClientProvider{Objects: []runtime.Object{pvcA, pvcB, podA, podB}}
	e, _ := NewWithKubernetesClientProvider(strategies, &kubernetesClientProvider)
	return e
}

func testStrategies() []strategy.Strategy {
	strategy1 := testutil.Strategy{
		NameVal:  "strategy1",
		CanDoVal: canDoTrue,
	}
	strategy2 := testutil.Strategy{
		NameVal:  "strategy2",
		CanDoVal: canDoTrue,
	}
	strategies := []strategy.Strategy{&strategy1, &strategy2}
	return strategies
}

func canDoTrue(_ job.Job) bool {
	return true
}

func canDoFalse(_ job.Job) bool {
	return false
}
