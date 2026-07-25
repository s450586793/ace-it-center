package domain

import "fmt"

const (
	NodeTypeWindows = "windows"
	NodeTypeLinux   = "linux"
)

func ValidateNodeType(nodeType string) error {
	switch nodeType {
	case NodeTypeWindows, NodeTypeLinux:
		return nil
	default:
		return fmt.Errorf("unsupported node type %q", nodeType)
	}
}

