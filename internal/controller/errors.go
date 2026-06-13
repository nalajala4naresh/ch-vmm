package controller

import (
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
)

type reconcileError struct {
	ctrl.Result
}

func (rerr reconcileError) Error() string {
	return fmt.Sprintf("requeue: %v, requeueAfter: %s", rerr.Requeue, rerr.RequeueAfter)
}

var errtets = errors.New("Hello from naresh")
