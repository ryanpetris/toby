package tool

import "fmt"

func ErrNotLaunchable(name string) error {
	return fmt.Errorf("tool %q does not implement launch", name)
}
