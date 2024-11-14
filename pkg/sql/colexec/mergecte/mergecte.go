// Copyright 2021 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mergecte

import (
	"bytes"
	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	"github.com/matrixorigin/matrixone/pkg/container/batch"
	"github.com/matrixorigin/matrixone/pkg/container/types"
	"github.com/matrixorigin/matrixone/pkg/container/vector"
	"github.com/matrixorigin/matrixone/pkg/vm"
	"github.com/matrixorigin/matrixone/pkg/vm/process"
)

const argName = "merge_cte"

func (arg *Argument) String(buf *bytes.Buffer) {
	buf.WriteString(argName)
	buf.WriteString(": merge cte ")
}

func (arg *Argument) Prepare(proc *process.Process) error {
	ap := arg
	ap.ctr = new(container)
	ap.ctr.InitReceiver(proc, true)
	ap.ctr.nodeCnt = int32(len(proc.Reg.MergeReceivers)) - 1
	ap.ctr.curNodeCnt = ap.ctr.nodeCnt
	ap.ctr.status = sendInitial
	ap.ctr.recursiveLever = 0
	return nil
}

func (arg *Argument) Call(proc *process.Process) (vm.CallResult, error) {
	if err, isCancel := vm.CancelCheck(proc); isCancel {
		return vm.CancelResult, err
	}

	anal := proc.GetAnalyze(arg.GetIdx(), arg.GetParallelIdx(), arg.GetParallelMajor())
	anal.Start()
	defer anal.Stop()
	var msg *process.RegisterMessage
	result := vm.NewCallResult()
	if arg.buf != nil {
		proc.PutBatch(arg.buf)
		arg.buf = nil
	}
	switch arg.ctr.status {
	case sendInitial:
		msg = arg.ctr.ReceiveFromSingleReg(0, anal)
		if msg.Err != nil {
			result.Status = vm.ExecStop
			return result, msg.Err
		}
		arg.buf = msg.Batch
		if arg.buf == nil {
			arg.ctr.status = sendLastTag
		}
		fallthrough
	case sendLastTag:
		if arg.ctr.status == sendLastTag {
			arg.ctr.status = sendRecursive
			arg.buf = makeRecursiveBatch(proc)
			arg.ctr.RemoveChosen(1)
		}
	case sendRecursive:
		for {
			msg = arg.ctr.ReceiveFromAllRegs(anal)
			if msg.Batch == nil {
				result.Batch = nil
				result.Status = vm.ExecStop
				return result, nil
			}
			arg.buf = msg.Batch
			if !arg.buf.Last() {
				break
			}

			arg.buf.SetLast()
			arg.ctr.curNodeCnt--
			if arg.ctr.curNodeCnt == 0 {
				arg.ctr.curNodeCnt = arg.ctr.nodeCnt
				arg.ctr.recursiveLever++
				if arg.ctr.recursiveLever > moDefaultRecursionMax {
					result.Status = vm.ExecStop
					return result, moerr.NewCheckRecursiveLevel(proc.Ctx)
				}
				break
			} else {
				proc.PutBatch(arg.buf)
			}
		}
	}

	anal.Input(arg.buf, arg.GetIsFirst())
	anal.Output(arg.buf, arg.GetIsLast())
	result.Batch = arg.buf
	return result, nil
}

func makeRecursiveBatch(proc *process.Process) *batch.Batch {
	b := batch.NewWithSize(1)
	b.Attrs = []string{
		"recursive_col",
	}
	b.SetVector(0, proc.GetVector(types.T_varchar.ToType()))
	vector.AppendBytes(b.GetVector(0), []byte("check recursive status"), false, proc.GetMPool())
	batch.SetLength(b, 1)
	b.SetLast()
	return b
}
