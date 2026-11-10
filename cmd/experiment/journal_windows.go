package main

import "fmt"

func lockJournal(string) (func() error, error) {
	return nil, fmt.Errorf("实验工具需要Linux容器")
}
