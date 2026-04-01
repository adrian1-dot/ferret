package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/adrian1-dot/ferret/internal/fsutil"
	"gopkg.in/yaml.v3"
)

const DefaultStatePath = ".ferret/state.yaml"

type State struct {
	Version int               `yaml:"version" json:"version"`
	Cursors map[string]string `yaml:"cursors" json:"cursors"`
}

type StateStore interface {
	Load(ctx context.Context) (*State, error)
	Save(ctx context.Context, state *State) error
}

type FileStateStore struct {
	Path string
}

func DefaultState() *State {
	return &State{
		Version: 1,
		Cursors: map[string]string{},
	}
}

func (s FileStateStore) Load(_ context.Context) (*State, error) {
	path := s.path()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultState(), nil
	}
	if err != nil {
		return nil, err
	}
	state := DefaultState()
	if err := yaml.Unmarshal(data, state); err != nil {
		return nil, err
	}
	if state.Cursors == nil {
		state.Cursors = map[string]string{}
	}
	return state, nil
}

func (s FileStateStore) Save(_ context.Context, state *State) error {
	if state.Cursors == nil {
		state.Cursors = map[string]string{}
	}
	path := s.path()
	data, err := yaml.Marshal(state)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, data)
}

func (s FileStateStore) path() string {
	if s.Path == "" {
		return ExpandPath(DefaultStatePath)
	}
	return ExpandPath(s.Path)
}

func StatePathForConfig(configPath string) string {
	configPath = ExpandPath(configPath)
	if configPath == "" || configPath == ExpandPath(DefaultConfigPath) {
		return DefaultStatePath
	}
	return filepath.Join(filepath.Dir(configPath), "state.yaml")
}
