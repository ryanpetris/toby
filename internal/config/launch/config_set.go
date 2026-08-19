package launchconfig

// Canonical project configuration-set discovery and loading. A canonical
// .toby/config.yaml is merged with regular .toby/config.d/*.yaml fragments in
// lexical order through directory-rooted file descriptors; arbitrary --config
// paths load only the exact selected file.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"petris.dev/toby/internal/config"
	configfile "petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/diagnostic/warning"

	"gopkg.in/yaml.v3"
)

const (
	projectConfigDirName    = ".toby"
	projectConfigFileName   = "config.yaml"
	projectFragmentsDir     = "config.d"
	projectLaunchConfigName = projectConfigDirName + "/" + projectConfigFileName

	yamlNormalizationBaseNodes    = 256
	yamlNormalizationNodesPerByte = 16
	yamlNormalizationMaxNodes     = 100_000
	yamlNormalizationMaxDepth     = 100
)

var launchSchemaType = reflect.TypeOf(launchSchema{})

func projectConfigMarker(
	projectRoot string,
	logger *diagnostic.Logger,
) (found bool, returnErr error) {
	project, err := os.OpenRoot(projectRoot)
	if err != nil {
		return false, err
	}
	defer func() {
		logger.DebugError("close project configuration root", project.Close())
	}()

	configDirPath := filepath.Join(projectRoot, projectConfigDirName)
	info, err := project.Lstat(projectConfigDirName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%s: %w", configDirPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s must not be a symbolic link", configDirPath)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s must be a directory", configDirPath)
	}

	configDir, err := openRootedDirectory(
		project,
		projectConfigDirName,
		configDirPath,
		logger,
	)
	if err != nil {
		return false, err
	}
	defer func() {
		logger.DebugError(
			"close project configuration directory",
			configDir.Close(),
		)
	}()

	configPath := filepath.Join(configDirPath, projectConfigFileName)
	info, err = configDir.Lstat(projectConfigFileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%s: %w", configPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s must not be a symbolic link", configPath)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s must be a regular file", configPath)
	}
	return true, nil
}

func loadLaunchConfig(
	path string,
	paths config.Paths,
	logger *diagnostic.Logger,
	warnings *warning.Service,
) (launchConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return launchConfig{}, exitcode.New(2, "--config requires a value")
	}

	paths = launchConfigPaths(paths)
	expanded := config.ExpandHome(path, paths.Home)
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return launchConfig{}, err
	}

	schema, relativeRoot, err := loadLaunchSchema(abs, logger, warnings)
	if err != nil {
		return launchConfig{}, err
	}

	cfg, err := parseLaunchConfigWithPaths(schema, relativeRoot, paths)
	if err != nil {
		return launchConfig{}, fmt.Errorf("%s: %w", abs, err)
	}
	return cfg, nil
}

func loadLaunchSchema(
	path string,
	logger *diagnostic.Logger,
	warnings *warning.Service,
) (launchSchema, string, error) {
	if isCanonicalProjectConfig(path) {
		projectRoot := filepath.Dir(filepath.Dir(path))
		schema, err := loadProjectConfigSet(projectRoot, logger, warnings)
		return schema, projectRoot, err
	}

	schema, err := loadSingleLaunchConfig(path, logger)
	return schema, filepath.Dir(path), err
}

func isCanonicalProjectConfig(path string) bool {
	configDir := filepath.Dir(path)
	return filepath.Base(path) == projectConfigFileName &&
		filepath.Base(configDir) == projectConfigDirName
}

func loadSingleLaunchConfig(
	path string,
	logger *diagnostic.Logger,
) (result launchSchema, returnErr error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return launchSchema{}, err
	}
	defer func() {
		logger.DebugError("close launch configuration root", root.Close())
	}()

	data, err := readRootedRegularFile(
		root,
		filepath.Base(path),
		path,
		logger,
	)
	if err != nil {
		return launchSchema{}, err
	}
	if _, _, err := parseLaunchSource(data, path); err != nil {
		return launchSchema{}, err
	}

	var schema launchSchema
	if err := configfile.Decode(data, configfile.FormatYAML, path, &schema); err != nil {
		return launchSchema{}, fmt.Errorf("%s: %w", path, err)
	}
	return schema, nil
}

func loadProjectConfigSet(
	projectRoot string,
	logger *diagnostic.Logger,
	warnings *warning.Service,
) (result launchSchema, returnErr error) {
	project, err := os.OpenRoot(projectRoot)
	if err != nil {
		return launchSchema{}, err
	}
	defer func() {
		logger.DebugError("close project configuration root", project.Close())
	}()

	configDirPath := filepath.Join(projectRoot, projectConfigDirName)
	configDir, err := openRootedDirectory(
		project,
		projectConfigDirName,
		configDirPath,
		logger,
	)
	if err != nil {
		return launchSchema{}, err
	}
	defer func() {
		logger.DebugError(
			"close project configuration directory",
			configDir.Close(),
		)
	}()

	basePath := filepath.Join(configDirPath, projectConfigFileName)
	base, err := readRootedRegularFile(
		configDir,
		projectConfigFileName,
		basePath,
		logger,
	)
	if err != nil {
		return launchSchema{}, err
	}

	var merged yaml.Node
	if err := decodeLaunchSource(base, basePath, &merged); err != nil {
		return launchSchema{}, err
	}
	if err := mergeProjectFragments(
		configDir,
		configDirPath,
		&merged,
		logger,
		warnings,
	); err != nil {
		return launchSchema{}, err
	}

	if merged.Kind == 0 {
		return launchSchema{}, nil
	}
	data, err := yaml.Marshal(&merged)
	if err != nil {
		return launchSchema{}, fmt.Errorf("%s: encode merged config: %w", basePath, err)
	}

	var schema launchSchema
	if err := configfile.Decode(data, configfile.FormatYAML, basePath, &schema); err != nil {
		return launchSchema{}, fmt.Errorf("%s: %w", basePath, err)
	}
	return schema, nil
}

func decodeLaunchSource(data []byte, path string, merged *yaml.Node) error {
	normalized, empty, err := parseLaunchSource(data, path)
	if err != nil {
		return err
	}
	if empty {
		return nil
	}

	if merged.Kind == 0 {
		*merged = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{normalized},
		}
		return nil
	}
	current, _, err := launchSourceMapping(merged, path)
	if err != nil {
		return err
	}
	mergeYAMLMapping(current, normalized)
	return nil
}

func parseLaunchSource(data []byte, path string) (*yaml.Node, bool, error) {
	var source yaml.Node
	if err := decodeSingleYAMLDocument(data, path, &source); err != nil {
		return nil, false, err
	}

	mapping, empty, err := launchSourceMapping(&source, path)
	if err != nil {
		return nil, false, err
	}
	if empty {
		return nil, true, nil
	}
	if err := validateLaunchYAML(mapping, path); err != nil {
		return nil, false, err
	}
	normalized, err := normalizeLaunchYAML(mapping, path, len(data))
	if err != nil {
		return nil, false, err
	}
	if err := validateLaunchYAML(normalized, path); err != nil {
		return nil, false, err
	}
	if err := validatePartialLaunchSchema(normalized, launchSchemaType, path, ""); err != nil {
		return nil, false, err
	}

	return normalized, false, nil
}

func decodeSingleYAMLDocument(data []byte, path string, dest any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("%s: %w", path, err)
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	return fmt.Errorf("%s: multiple YAML documents are not allowed", path)
}

func launchSourceMapping(document *yaml.Node, path string) (*yaml.Node, bool, error) {
	if document == nil || document.Kind == 0 || len(document.Content) == 0 {
		return nil, true, nil
	}

	root := document
	if root.Kind == yaml.DocumentNode {
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return nil, true, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("%s: launch config must be a mapping", path)
	}
	return root, false, nil
}

func validateLaunchYAML(node *yaml.Node, path string) error {
	pending := []*yaml.Node{node}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]

		switch current.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			pending = append(pending, current.Content...)
		case yaml.MappingNode:
			seen := make(map[string]bool, len(current.Content)/2)
			for i := 0; i < len(current.Content); i += 2 {
				key := current.Content[i]
				if key.Kind != yaml.ScalarNode {
					return fmt.Errorf("%s:%d: config keys must be scalars", path, key.Line)
				}
				identity := key.Tag + "\x00" + key.Value
				if seen[identity] {
					return fmt.Errorf("%s:%d: duplicate config key %q", path, key.Line, key.Value)
				}
				seen[identity] = true
				pending = append(pending, current.Content[i+1])
			}
		}
	}
	return nil
}

// validatePartialLaunchSchema checks one source against the launch schema
// without decoding it as a complete config. This keeps partial fragments valid
// while ensuring a later fragment cannot hide an earlier source's type or
// unknown-field error.
func validatePartialLaunchSchema(node *yaml.Node, target reflect.Type, sourcePath, fieldPath string) error {
	if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null" {
		return nil
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}

	switch target.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return yamlTypeError(node, target, sourcePath, fieldPath)
		}

		fields := yamlStructFields(target)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			fieldType, ok := fields[key.Value]
			if !ok {
				return fmt.Errorf("%s:%d: field %s not found in type %s", sourcePath, key.Line, key.Value, target)
			}

			childPath := key.Value
			if fieldPath != "" {
				childPath = fieldPath + "." + key.Value
			}
			if err := validatePartialLaunchSchema(node.Content[i+1], fieldType, sourcePath, childPath); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return yamlTypeError(node, target, sourcePath, fieldPath)
		}

		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if err := validateYAMLScalar(key, target.Key(), sourcePath, fieldPath); err != nil {
				return err
			}

			childPath := key.Value
			if fieldPath != "" {
				childPath = fieldPath + "." + key.Value
			}
			if err := validatePartialLaunchSchema(node.Content[i+1], target.Elem(), sourcePath, childPath); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return yamlTypeError(node, target, sourcePath, fieldPath)
		}
		for i, child := range node.Content {
			childPath := fmt.Sprintf("%s[%d]", fieldPath, i)
			if err := validatePartialLaunchSchema(child, target.Elem(), sourcePath, childPath); err != nil {
				return err
			}
		}
		return nil
	case reflect.Interface:
		return nil
	default:
		return validateYAMLScalar(node, target, sourcePath, fieldPath)
	}
}

func yamlStructFields(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < target.NumField(); i++ {
		field := target.Field(i)
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("yaml")
		name, options, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if hasYAMLOption(options, "inline") {
			inlineType := field.Type
			for inlineType.Kind() == reflect.Pointer {
				inlineType = inlineType.Elem()
			}
			if inlineType.Kind() == reflect.Struct {
				for inlineName, inlineField := range yamlStructFields(inlineType) {
					fields[inlineName] = inlineField
				}
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		fields[name] = field.Type
	}
	return fields
}

func hasYAMLOption(options, target string) bool {
	for options != "" {
		option, remainder, _ := strings.Cut(options, ",")
		if option == target {
			return true
		}
		options = remainder
	}
	return false
}

func validateYAMLScalar(node *yaml.Node, target reflect.Type, sourcePath, fieldPath string) error {
	if node.Kind != yaml.ScalarNode {
		return yamlTypeError(node, target, sourcePath, fieldPath)
	}

	value := reflect.New(target)
	if err := node.Decode(value.Interface()); err != nil {
		if fieldPath == "" {
			return fmt.Errorf("%s:%d: %w", sourcePath, node.Line, err)
		}
		return fmt.Errorf("%s:%d: %s: %w", sourcePath, node.Line, fieldPath, err)
	}
	return nil
}

func yamlTypeError(node *yaml.Node, target reflect.Type, sourcePath, fieldPath string) error {
	description := node.ShortTag()
	if description == "" {
		description = fmt.Sprintf("YAML node kind %d", node.Kind)
	}
	if fieldPath == "" {
		return fmt.Errorf("%s:%d: cannot unmarshal %s into %s", sourcePath, node.Line, description, target)
	}
	return fmt.Errorf("%s:%d: %s: cannot unmarshal %s into %s", sourcePath, node.Line, fieldPath, description, target)
}

type yamlNormalizationBudget struct {
	nodes int
	depth int
}

func normalizeLaunchYAML(node *yaml.Node, path string, sourceBytes int) (*yaml.Node, error) {
	// Ordinary nodes are far fewer than source bytes. The proportional allowance
	// permits normal anchor reuse while the hard cap bounds acyclic alias bombs.
	nodeLimit := yamlNormalizationMaxNodes
	if sourceBytes < (yamlNormalizationMaxNodes-yamlNormalizationBaseNodes)/yamlNormalizationNodesPerByte {
		nodeLimit = yamlNormalizationBaseNodes + sourceBytes*yamlNormalizationNodesPerByte
	}
	budget := &yamlNormalizationBudget{nodes: nodeLimit}
	return normalizeYAMLNode(node, path, map[*yaml.Node]bool{}, budget)
}

func normalizeYAMLNode(node *yaml.Node, path string, active map[*yaml.Node]bool, budget *yamlNormalizationBudget) (*yaml.Node, error) {
	if node == nil {
		return nil, fmt.Errorf("%s: invalid empty YAML node", path)
	}
	if budget.nodes == 0 {
		return nil, fmt.Errorf("%s:%d: normalized YAML exceeds the configuration size limit", path, node.Line)
	}
	budget.nodes--
	if budget.depth == yamlNormalizationMaxDepth {
		return nil, fmt.Errorf("%s:%d: YAML nesting exceeds the depth limit", path, node.Line)
	}
	budget.depth++
	defer func() {
		budget.depth--
	}()

	if active[node] {
		return nil, fmt.Errorf("%s:%d: cyclic YAML alias", path, node.Line)
	}
	active[node] = true
	defer delete(active, node)

	if node.Kind == yaml.AliasNode {
		if node.Alias == nil {
			return nil, fmt.Errorf("%s:%d: YAML alias has no target", path, node.Line)
		}
		return normalizeYAMLNode(node.Alias, path, active, budget)
	}

	clone := *node
	clone.Anchor = ""
	clone.Alias = nil
	clone.Content = nil
	if node.Kind == yaml.MappingNode {
		if err := normalizeYAMLMapping(node, &clone, path, active, budget); err != nil {
			return nil, err
		}
		return &clone, nil
	}

	for _, child := range node.Content {
		normalized, err := normalizeYAMLNode(child, path, active, budget)
		if err != nil {
			return nil, err
		}
		clone.Content = append(clone.Content, normalized)
	}
	return &clone, nil
}

func normalizeYAMLMapping(src, dst *yaml.Node, path string, active map[*yaml.Node]bool, budget *yamlNormalizationBudget) error {
	seen := make(map[string]bool, len(src.Content)/2)
	var merges []*yaml.Node
	for i := 0; i < len(src.Content); i += 2 {
		key := src.Content[i]
		if isYAMLMergeKey(key) {
			merges = append(merges, src.Content[i+1])
			continue
		}

		normalizedKey, err := normalizeYAMLNode(key, path, active, budget)
		if err != nil {
			return err
		}
		normalizedValue, err := normalizeYAMLNode(src.Content[i+1], path, active, budget)
		if err != nil {
			return err
		}
		dst.Content = append(dst.Content, normalizedKey, normalizedValue)
		seen[yamlKeyIdentity(normalizedKey)] = true
	}

	for _, merge := range merges {
		mappings, err := normalizeYAMLMerge(merge, path, active, budget)
		if err != nil {
			return err
		}
		for _, mapping := range mappings {
			for i := 0; i < len(mapping.Content); i += 2 {
				key := mapping.Content[i]
				identity := yamlKeyIdentity(key)
				if seen[identity] {
					continue
				}
				dst.Content = append(dst.Content, key, mapping.Content[i+1])
				seen[identity] = true
			}
		}
	}
	return nil
}

func normalizeYAMLMerge(node *yaml.Node, path string, active map[*yaml.Node]bool, budget *yamlNormalizationBudget) ([]*yaml.Node, error) {
	normalized, err := normalizeYAMLNode(node, path, active, budget)
	if err != nil {
		return nil, err
	}
	switch normalized.Kind {
	case yaml.MappingNode:
		return []*yaml.Node{normalized}, nil
	case yaml.SequenceNode:
		mappings := make([]*yaml.Node, 0, len(normalized.Content))
		for _, child := range normalized.Content {
			if child.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s:%d: YAML merge must contain mappings", path, child.Line)
			}
			mappings = append(mappings, child)
		}
		return mappings, nil
	default:
		return nil, fmt.Errorf("%s:%d: YAML merge must be a mapping or sequence of mappings", path, normalized.Line)
	}
}

func isYAMLMergeKey(key *yaml.Node) bool {
	if key == nil || key.Kind != yaml.ScalarNode || key.Value != "<<" {
		return false
	}
	switch key.Tag {
	case "", "!", "!!merge", "tag:yaml.org,2002:merge":
		return true
	default:
		return false
	}
}

func mergeYAMLMapping(dst, src *yaml.Node) {
	index := make(map[string]int, len(dst.Content)/2)
	for i := 0; i < len(dst.Content); i += 2 {
		index[yamlKeyIdentity(dst.Content[i])] = i
	}

	for i := 0; i < len(src.Content); i += 2 {
		srcKey := src.Content[i]
		srcValue := src.Content[i+1]
		dstIndex, ok := index[yamlKeyIdentity(srcKey)]
		if !ok {
			index[yamlKeyIdentity(srcKey)] = len(dst.Content)
			dst.Content = append(dst.Content, srcKey, srcValue)
			continue
		}

		dstValue := dst.Content[dstIndex+1]
		if dstValue.Kind == yaml.MappingNode && srcValue.Kind == yaml.MappingNode {
			mergeYAMLMapping(dstValue, srcValue)
			continue
		}
		dst.Content[dstIndex] = srcKey
		dst.Content[dstIndex+1] = srcValue
	}
}

func yamlKeyIdentity(key *yaml.Node) string {
	return key.Tag + "\x00" + key.Value
}

func mergeProjectFragments(
	configDir *os.Root,
	configDirPath string,
	merged *yaml.Node,
	logger *diagnostic.Logger,
	warnings *warning.Service,
) (returnErr error) {
	fragmentsPath := filepath.Join(configDirPath, projectFragmentsDir)
	info, err := configDir.Lstat(projectFragmentsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s: %w", fragmentsPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symbolic link", fragmentsPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory", fragmentsPath)
	}

	fragments, err := openRootedDirectory(
		configDir,
		projectFragmentsDir,
		fragmentsPath,
		logger,
	)
	if err != nil {
		return err
	}
	defer func() {
		logger.DebugError(
			"close project configuration fragments directory",
			fragments.Close(),
		)
	}()

	dir, err := fragments.Open(".")
	if err != nil {
		return fmt.Errorf("%s: %w", fragmentsPath, err)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return fmt.Errorf("%s: %w", fragmentsPath, readErr)
	}
	logger.DebugError(
		"close project configuration fragment listing",
		closeErr,
		"path",
		fragmentsPath,
	)

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(fragmentsPath, name)
		if filepath.Ext(name) != ".yaml" {
			if !entry.IsDir() && warnings != nil {
				warnings.Warn(
					warning.ConfigFragmentIgnored,
					fmt.Sprintf(
						"project configuration fragment %s is ignored; only .yaml files in config.d are loaded",
						path,
					),
					"path", path,
				)
			}
			continue
		}
		data, err := readRootedRegularFile(
			fragments,
			name,
			path,
			logger,
		)
		if err != nil {
			return err
		}
		if err := decodeLaunchSource(data, path, merged); err != nil {
			return err
		}
	}
	return nil
}

func openRootedDirectory(
	parent *os.Root,
	name string,
	path string,
	logger *diagnostic.Logger,
) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must not be a symbolic link", path)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("%s must be a directory", path)
	}

	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	after, err := root.Stat(".")
	if err != nil {
		logger.DebugError(
			"close rooted configuration directory",
			root.Close(),
			"path",
			path,
		)
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !os.SameFile(before, after) {
		logger.DebugError(
			"close replaced configuration directory",
			root.Close(),
			"path",
			path,
		)
		return nil, fmt.Errorf("%s changed while loading", path)
	}
	return root, nil
}

func readRootedRegularFile(
	root *os.Root,
	name string,
	path string,
	logger *diagnostic.Logger,
) (result []byte, returnErr error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must not be a symbolic link", path)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}

	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() {
		logger.DebugError(
			"close launch configuration file",
			file.Close(),
			"path",
			path,
		)
	}()

	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !after.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	if !os.SameFile(before, after) {
		return nil, fmt.Errorf("%s changed while loading", path)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, nil
}
