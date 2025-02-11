package gardenruntimetest

import (
	"strconv"
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/gardenruntime"
	"github.com/concourse/concourse/atc/worker/workertest"

	. "github.com/onsi/gomega"
)

type DBState int

const (
	_ DBState = iota
	Creating
	Created
)

type SetupFunc func(*Worker, *workertest.Scenario)
type WorkerSetupFunc func(*atc.Worker)

type Worker struct {
	WorkerName       string
	Containers       []*Container
	Volumes          []*Volume
	SetupFuncs       []SetupFunc
	WorkerSetupFuncs []WorkerSetupFunc
}

func NewWorker(name string) *Worker {
	return &Worker{WorkerName: name}
}

func (w Worker) Name() string {
	return w.WorkerName
}

func (w *Worker) Setup(s *workertest.Scenario) {
	atcWorker := dbtest.BaseWorker(w.Name())
	atcWorker.ActiveContainers = len(w.Containers)
	atcWorker.ActiveVolumes = len(w.Volumes)

	for _, f := range w.WorkerSetupFuncs {
		f(&atcWorker)
	}

	s.DB.Run(s.DBBuilder.WithWorker(atcWorker))

	for _, f := range w.SetupFuncs {
		s.Run(func(s *workertest.Scenario) { f(w, s) })
	}
}

func (w Worker) Build(db worker.DB, dbWorker db.Worker) runtime.Worker {
	return gardenruntime.NewWorker(
		dbWorker,
		&Garden{ContainerList: w.Containers},
		&Baggageclaim{Volumes: w.Volumes, Mutex: sync.Mutex{}},
		db.ToGardenRuntimeDB(),
		worker.NewStreamer(db.ResourceCacheFactory, compression.NewGzipCompression(), worker.P2PConfig{
			Enabled: false,
		}),
	)
}

func (w Worker) WithGardenContainers(containers ...*Container) *Worker {
	w2 := w
	w2.Containers = make([]*Container, len(w.Containers)+len(containers))
	copy(w2.Containers, w.Containers)
	copy(w2.Containers[len(w.Containers):], containers)
	return &w2
}

func (w Worker) WithBaggageclaimVolumes(volumes ...*Volume) *Worker {
	w2 := w
	w2.Volumes = make([]*Volume, len(w.Volumes)+len(volumes))
	copy(w2.Volumes, w.Volumes)
	copy(w2.Volumes[len(w.Volumes):], volumes)
	return &w2
}

func (w Worker) WithMutableSetup(setup ...SetupFunc) *Worker {
	w2 := w
	w2.SetupFuncs = make([]SetupFunc, len(w.SetupFuncs)+len(setup))
	copy(w2.SetupFuncs, w.SetupFuncs)
	copy(w2.SetupFuncs[len(w.SetupFuncs):], setup)
	return &w2
}

func (w Worker) WithSetup(setup ...workertest.SetupFunc) *Worker {
	workerSetup := make([]SetupFunc, len(setup))
	for i, f := range setup {
		workerSetup[i] = func(_ *Worker, s *workertest.Scenario) { f(s) }
	}
	return w.WithMutableSetup(workerSetup...)
}

func (w Worker) WithWorkerSetup(setup ...WorkerSetupFunc) *Worker {
	w2 := w
	w2.WorkerSetupFuncs = make([]WorkerSetupFunc, len(w.WorkerSetupFuncs)+len(setup))
	copy(w2.WorkerSetupFuncs, w.WorkerSetupFuncs)
	copy(w2.WorkerSetupFuncs[len(w.WorkerSetupFuncs):], setup)
	return &w2
}

func (w Worker) WithDBContainerVolumesInState(state DBState, containerHandle string, paths ...string) *Worker {
	return w.WithSetup(func(s *workertest.Scenario) {
		containerOwner := db.NewFixedHandleContainerOwner(containerHandle)
		container := s.DB.Container(w.Name(), containerOwner).(db.CreatingContainer)

		for _, path := range paths {
			volume, err := s.DBBuilder.VolumeRepo.CreateContainerVolume(s.TeamID, w.Name(), container, path)
			Expect(err).ToNot(HaveOccurred())

			if state == Created {
				_, err := volume.Created()
				Expect(err).ToNot(HaveOccurred())
			}
		}
	})
}

func (w Worker) WithDBContainersInState(state DBState, handles ...string) *Worker {
	return w.WithSetup(func(s *workertest.Scenario) {
		for _, handle := range handles {
			owner := db.NewFixedHandleContainerOwner(handle)
			switch state {
			case Creating:
				s.DB.Run(s.DBBuilder.WithCreatingContainer(w.Name(), owner, db.ContainerMetadata{}))
			case Created:
				s.DB.Run(s.DBBuilder.WithCreatedContainer(w.Name(), owner, db.ContainerMetadata{}))
			default:
				panic("invalid state " + strconv.Itoa(int(state)))
			}
		}
	})
}

func (w Worker) WithDBVolumesInState(state DBState, handles ...string) *Worker {
	return w.WithSetup(func(s *workertest.Scenario) {
		for _, handle := range handles {
			switch state {
			case Creating:
				s.DB.Run(s.DBBuilder.WithCreatingVolume(s.TeamID, w.Name(), db.VolumeTypeContainer, handle))
			case Created:
				s.DB.Run(s.DBBuilder.WithCreatedVolume(s.TeamID, w.Name(), db.VolumeTypeContainer, handle))
			default:
				panic("invalid state " + strconv.Itoa(int(state)))
			}
		}
	})
}

func (w Worker) WithContainersCreatedInDBAndGarden(containers ...*Container) *Worker {
	return w.WithGardenContainers(containers...).WithDBContainersInState(Created, containerHandles(containers)...)
}

func (w Worker) WithVolumesCreatedInDBAndBaggageclaim(volumes ...*Volume) *Worker {
	return w.WithBaggageclaimVolumes(volumes...).WithDBVolumesInState(Created, volumeHandles(volumes)...)
}

func (w Worker) WithJobBuildContainerCreatedInDBAndGarden() *Worker {
	return w.WithMutableSetup(func(w *Worker, s *workertest.Scenario) {
		var container db.CreatingContainer
		s.DB.Run(s.DBBuilder.WithJobBuildContainer(&container, s.JobName, w.WorkerName, s.TeamID))

		created, err := container.Created()
		Expect(err).ToNot(HaveOccurred())

		*w = *w.WithGardenContainers(NewContainer(created.Handle()))
	})
}

func (w Worker) WithActiveTasks(activeTasks int) *Worker {
	return w.WithSetup(func(s *workertest.Scenario) {
		worker := s.DB.Worker(w.Name())
		for i := 0; i < activeTasks; i++ {
			_, err := worker.IncreaseActiveTasks(activeTasks)
			Expect(err).ToNot(HaveOccurred())
		}
	})
}

func (w Worker) WithTeam(team string) *Worker {
	return w.WithWorkerSetup(func(w *atc.Worker) {
		w.Team = team
	})
}

func (w Worker) WithState(state db.WorkerState) *Worker {
	return w.WithWorkerSetup(func(w *atc.Worker) {
		w.State = string(state)
	})
}

func (w Worker) WithTags(tags ...string) *Worker {
	return w.WithWorkerSetup(func(w *atc.Worker) {
		w.Tags = append(w.Tags, tags...)
	})
}

func (w Worker) WithPlatform(platform string) *Worker {
	return w.WithWorkerSetup(func(w *atc.Worker) {
		w.Platform = platform
	})
}

func (w Worker) WithVersion(version string) *Worker {
	return w.WithWorkerSetup(func(w *atc.Worker) {
		w.Version = version
	})
}

func containerHandles(containers []*Container) []string {
	handles := make([]string, len(containers))
	for i, c := range containers {
		handles[i] = c.handle
	}
	return handles
}

func volumeHandles(volumes []*Volume) []string {
	handles := make([]string, len(volumes))
	for i, v := range volumes {
		handles[i] = v.handle
	}
	return handles
}
