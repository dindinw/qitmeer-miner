/**
Qitmeer
james
*/
package qitmeer

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/Qitmeer/go-opencl/cl"
	"github.com/Qitmeer/qitmeer-lib/core/types/pow"
	"math/big"
	"qitmeer-miner/common"
	"qitmeer-miner/core"
	"qitmeer-miner/kernel"
	"time"
)

type Blake2bD struct {
	core.Device
	Work    *QitmeerWork
	header MinerBlockData
}

func (this *Blake2bD) InitDevice() {
	this.Started = time.Now().Unix()
	this.Device.InitDevice()
	if !this.IsValid {
		return
	}
	var err error
	this.Program, err = this.Context.CreateProgramWithSource([]string{kernel.DoubleBlake2bKernelSource})
	if err != nil {
		common.MinerLoger.Errorf("#-%d,%s,%v CreateProgramWithSource", this.MinerId, this.DeviceName, err)
		this.IsValid = false
		return
	}

	err = this.Program.BuildProgram([]*cl.Device{this.ClDevice}, "")
	if err != nil {
		common.MinerLoger.Errorf("-%d,%v BuildProgram", this.MinerId, err)
		this.IsValid = false
		return
	}

	this.Kernel, err = this.Program.CreateKernel("search")
	if err != nil {
		common.MinerLoger.Errorf("-%d,%v CreateKernel", this.MinerId, err)
		this.IsValid = false
		return
	}
	this.BlockObj, err = this.Context.CreateEmptyBuffer(cl.MemReadOnly, 128)
	if err != nil {
		common.MinerLoger.Errorf("-%d,%v CreateEmptyBuffer BlockObj", this.MinerId, err)
		this.IsValid = false
		return
	}
	_ = this.Kernel.SetArgBuffer(0, this.BlockObj)
	this.NonceOutObj, err = this.Context.CreateEmptyBuffer(cl.MemReadWrite, 8)
	if err != nil {
		common.MinerLoger.Errorf("-%d,%v CreateEmptyBuffer NonceOutObj", this.MinerId, err)
		this.IsValid = false
		return
	}
	_= this.Kernel.SetArgBuffer(1, this.NonceOutObj)
	this.NonceRandObj, err = this.Context.CreateEmptyBuffer(cl.MemReadWrite, 8)
	if err != nil {
		common.MinerLoger.Errorf("-%d,%v CreateEmptyBuffer NonceRandObj", this.MinerId, err)
		this.IsValid = false
		return
	}
	this.Target2Obj, err = this.Context.CreateEmptyBuffer(cl.MemReadWrite, 32)
	if err != nil {
		common.MinerLoger.Errorf("-%d,%v CreateEmptyBuffer Target2Obj", this.MinerId, err)
		this.IsValid = false
		return
	}
	_ = this.Kernel.SetArgBuffer(1, this.NonceOutObj)
	this.LocalItemSize = this.Cfg.OptionConfig.WorkSize
	_ = this.Kernel.SetArgBuffer(2, this.NonceRandObj)
	_ = this.Kernel.SetArgBuffer(3, this.Target2Obj)
	common.MinerLoger.Debugf("- Device ID:%d- Global item size:%d- Local item size:%d",this.MinerId, this.GlobalItemSize, this.LocalItemSize)
	this.NonceOut = make([]byte, 8)
	if _, err = this.CommandQueue.EnqueueWriteBufferByte(this.NonceOutObj, true, 0, this.NonceOut, nil); err != nil {
		common.MinerLoger.Errorf("-%d %v EnqueueWriteBufferByte NonceOutObj", this.MinerId, err)
		this.IsValid = false
		return
	}
}

func (this *Blake2bD) Update() {
	//update coinbase tx hash
	this.Device.Update()
	if this.Pool {
		this.Work.PoolWork.ExtraNonce2 = fmt.Sprintf("%08x", this.CurrentWorkID)
		this.Work.PoolWork.WorkData = this.Work.PoolWork.PrepQitmeerWork()
		this.header.PackagePoolHeader(this.Work,pow.BLAKE2BD)
	} else {
		randStr := fmt.Sprintf("%s%d",this.Cfg.SoloConfig.RandStr,this.CurrentWorkID)
		_ = this.Work.Block.CalcCoinBase(this.Cfg,randStr,this.CurrentWorkID,this.Cfg.SoloConfig.MinerAddr)
		this.Work.PoolWork.ExtraNonce2 = fmt.Sprintf("%08x", uint32(this.CurrentWorkID))
		this.header.Exnonce2 = this.Work.PoolWork.ExtraNonce2
		this.Work.PoolWork.WorkData = this.Work.PoolWork.PrepQitmeerWork()
		txHash := this.Work.Block.BuildMerkleTreeStore(int(this.MinerId))
		this.header.PackageRpcHeader(this.Work)
		this.header.HeaderBlock.TxRoot = txHash
	}
}

func (this *Blake2bD) Mine() {
	defer this.Release()
	for {

		select {
		case w := <-this.NewWork:
			this.Work = w.(*QitmeerWork)
		case <-this.Quit:
			return
		default:

		}
		if this.Cfg.OptionConfig.Restart == 1{
			common.MinerLoger.Errorf("device # %d mining listen restart",this.MinerId)
			return
		}
		if !this.IsValid {
			common.MinerLoger.Errorf("# %d %s device not use to mining.",this.MinerId,this.DeviceName)
			time.Sleep(2*time.Second)
			continue
		}
		if !this.HasNewWork || this.Work == nil{
			continue
		}
		if len(this.Work.PoolWork.WorkData) <= 0 && this.Work.Block.Height <= 0 {
			continue
		}
		this.Started = time.Now().Unix()
		this.AllDiffOneShares = 0
		this.HasNewWork = false
		offset := 0
		this.CurrentWorkID = 0
		this.header = MinerBlockData{
			Transactions:[]Transactions{},
			Parents:[]ParentItems{},
			HeaderData:make([]byte,0),
			TargetDiff:&big.Int{},
			JobID:"",
		}
		for {
			// if has new work ,current calc stop
			if this.HasNewWork {
				break
			}
			this.Update()
			var err error
			if _, err = this.CommandQueue.EnqueueWriteBufferByte(this.BlockObj, true, 0, this.header.HeaderBlock.BlockData(), nil); err != nil {
				common.MinerLoger.Errorf("-%d %v", this.MinerId, err)
				this.IsValid = false
				return
			}
			if !this.IsValid {
				break
			}
			if this.Cfg.OptionConfig.Restart == 1{
				common.MinerLoger.Errorf("device # %d mining restart",this.MinerId)
				return
			}

			randNonceBase,_ := common.RandUint64()
			randNonceBytes := make([]byte,8)
			binary.LittleEndian.PutUint64(randNonceBytes,randNonceBase)
			if _, err = this.CommandQueue.EnqueueWriteBufferByte(this.NonceRandObj, true, 0, randNonceBytes, nil); err != nil {
				common.MinerLoger.Errorf("-%d %v EnqueueWriteBufferByte NonceRandObj", this.MinerId, err)
				this.IsValid = false
				return
			}
			if _, err = this.CommandQueue.EnqueueWriteBufferByte(this.Target2Obj, true, 0, this.header.Target2, nil); err != nil {
				common.MinerLoger.Errorf("-%d %v EnqueueWriteBufferByte Target2Obj", this.MinerId, err)
				this.IsValid = false
				return
			}
			//Run the kernel
			if _, err = this.CommandQueue.EnqueueNDRangeKernel(this.Kernel, []int{int(offset)}, []int{this.GlobalItemSize}, []int{this.LocalItemSize}, nil); err != nil {
				common.MinerLoger.Errorf("-%d %v EnqueueNDRangeKernel Kernel", this.MinerId, err)
				this.IsValid = false
				return
			}
			//offset++
			//Get output
			if _, err = this.CommandQueue.EnqueueReadBufferByte(this.NonceOutObj, true, 0, this.NonceOut, nil); err != nil {
				common.MinerLoger.Errorf("-%d %v EnqueueReadBufferByte NonceOutObj", this.MinerId, err)
				this.IsValid = false
				return
			}
			this.AllDiffOneShares += uint64(this.GlobalItemSize)
			xnonce := binary.LittleEndian.Uint64(this.NonceOut)
			if xnonce >0 {
				//Found Hash
				this.header.HeaderBlock.Pow.SetNonce(binary.LittleEndian.Uint64(this.NonceOut))
				h := this.header.HeaderBlock.BlockHash()
				headerData := BlockDataWithProof(this.header.HeaderBlock)
				common.MinerLoger.Debugf("device #%d found hash:%s nonce:%d target:%064x",this.MinerId,h,xnonce,this.header.TargetDiff)
				if HashToBig(&h).Cmp(this.header.TargetDiff) <= 0 {
					subm := hex.EncodeToString(headerData)
					if !this.Pool{
						subm += common.Int2varinthex(int64(len(this.header.Parents)))
						for j := 0; j < len(this.header.Parents); j++ {
							subm += this.header.Parents[j].Data
						}

						txCount := len(this.header.Transactions) //real transaction count except coinbase
						subm += common.Int2varinthex(int64(txCount))

						for j := 0; j < txCount; j++ {
							subm += this.header.Transactions[j].Data
						}
						txCount -= 1
						subm += "-" + fmt.Sprintf("%d",txCount) + "-" + fmt.Sprintf("%d",this.header.Exnonce2)
					} else {
						subm += "-" + this.header.JobID + "-" + this.header.Exnonce2
					}
					this.SubmitData <- subm
					if !this.Pool{
						//solo wait new task
						this.ClearNonceData()
						break
					}
				}
			}
			this.ClearNonceData()
		}
	}
}

func (this* Blake2bD) ClearNonceData()  {
	this.NonceOut = make([]byte, 8)
	if _, err := this.CommandQueue.EnqueueWriteBufferByte(this.NonceOutObj, true, 0, this.NonceOut, nil); err != nil {
		common.MinerLoger.Errorf("-%d %v EnqueueWriteBufferByte", this.MinerId, err)
		this.IsValid = false
		return
	}
}