package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"

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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (s FileStateStore) path() string {
	if s.Path == "" {
		return DefaultStatePath
	}
	return s.Path
}

func StatePathForConfig(configPath string) string {
	if configPath == "" || configPath == DefaultConfigPath {
		return DefaultStatePath
	}
	return filepath.Join(filepath.Dir(configPath), "state.yaml")
}
