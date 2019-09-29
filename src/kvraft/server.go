package raftkv

import (
	"context"
	"labgob"
	"labrpc"
	"log"
	"raft"
	"sync"
)

const Debug = 0

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug > 0 {
		log.Printf(format, a...)
	}
	return
}

type OPCode string

const (
	GET    = "Get"
	PUT    = "Put"
	APPEND = "Append"
)

type ClerkTrackAction int

const (
	_ ClerkTrackAction = iota
	ClerkOK
	ClerkIgnore
	ClerkRetry
)

type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	OpCode   OPCode
	ServerId int
	ClerkId  int64
	SeqId    int
	Key      string
	Value    string
}

type KVRPCReq struct {
	OpCode OPCode
	args   interface{}
	reply  interface{}
	done   chan struct{}
}

type KVRPCResp struct {
	wrongLeader bool
	leader      int
	err         Err
	value       string
	op          *Op
	done        chan struct{}
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	maxraftstate int // snapshot if log grows this big

	// Your definitions here.
	booting    bool
	db         map[string]string
	clerkTrack map[int64]int
	ctx        context.Context
	cancel     func()
	issueing   chan *KVRPCReq
	committing chan *KVRPCResp
}

func (kv *KVServer) serveRPC(opcode OPCode, args interface{}, reply interface{}) {
	req := KVRPCReq{
		opcode,
		args,
		reply,
		make(chan struct{}),
	}
	kv.issueing <- &req
	<-req.done
}

func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	kv.serveRPC(GET, args, reply)
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	kv.serveRPC((OPCode)(args.Op), args, reply)
}

func (kv *KVServer) waitingCommit(op *Op) KVRPCResp {
	commit := KVRPCResp{
		true,
		kv.me,
		"",
		"",
		op,
		make(chan struct{}),
	}
	kv.committing <- &commit
	DPrintf("Waiting %s commitProcess me: %d %+v", op.OpCode, kv.me, op)
	<-commit.done
	return commit
}

func (kv *KVServer) checkClerkTrack(clerkId int64, sedId int) ClerkTrackAction {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	v, ok := kv.clerkTrack[clerkId]
	//when restart
	if !ok && sedId > 0 || sedId > v+1 {
		return ClerkRetry
	}
	//for restart corner case
	if !ok && sedId == 0 || sedId == v+1 {
		if kv.booting {
			kv.booting = false
			return ClerkRetry
		}
		return ClerkOK
	}
	return ClerkIgnore
}

func (kv *KVServer) issueToRAFT(req *KVRPCReq) {
	switch req.OpCode {
	case GET:
		args, reply := req.args.(*GetArgs), req.reply.(*GetReply)
		reply.Server = kv.me
		DPrintf("get Get me: %d %+v %+v", kv.me, args, reply)
		op := Op{GET, kv.me, args.ClerkId, args.SeqId, args.Key, ""}
		_, _, isLeader, leader := kv.rf.Start(op)
		if !isLeader {
			reply.WrongLeader = true
			reply.Leader = leader
			DPrintf("NotLeader Get me: %d %+v %+v", kv.me, args, reply)
			return
		}
		commit := kv.waitingCommit(&op)
		reply.Err = commit.err
		reply.WrongLeader = commit.wrongLeader
		reply.Leader = commit.leader
		reply.Value = commit.value
		DPrintf("reply Get me: %d %+v %+v", kv.me, args, reply)
		break
	case PUT, APPEND:
		args, reply := req.args.(*PutAppendArgs), req.reply.(*PutAppendReply)
		reply.Server = kv.me
		switch kv.checkClerkTrack(args.ClerkId, args.SeqId) {
		case ClerkIgnore:
			DPrintf("ignore PutAppend me: %d %+v %+v", kv.me, args, reply)
			return
		case ClerkRetry:
			reply.WrongLeader = true
			reply.Leader = -1
			DPrintf("retry PutAppend me: %d %+v %+v", kv.me, args, reply)
			return
		}
		DPrintf("get PutAppend me: %d %+v %+v", kv.me, args, reply)
		op := Op{(OPCode)(args.Op), kv.me, args.ClerkId, args.SeqId, args.Key, args.Value}
		_, _, isLeader, leader := kv.rf.Start(op)
		if !isLeader {
			reply.WrongLeader = true
			reply.Leader = leader
			DPrintf("NotLeader PutAppend me: %d %+v %+v", kv.me, args, reply)
			return
		}
		commit := kv.waitingCommit(&op)
		reply.Err = commit.err
		reply.WrongLeader = commit.wrongLeader
		reply.Leader = commit.leader
		DPrintf("reply PutAppend me: %d %+v %+v", kv.me, args, reply)
		break
	}
}

func (kv *KVServer) rpcProcess() {
	for {
		select {
		case rpc := <-kv.issueing:
			kv.issueToRAFT(rpc)
			rpc.done <- struct{}{}
			break
		case <-kv.ctx.Done():
			return
		}
	}
}

func (kv *KVServer) execute(op *Op) (string, Err) {
	switch op.OpCode {
	case PUT:
		kv.db[op.Key] = op.Value
		break
	case GET:
		v, exist := kv.db[op.Key]
		if !exist {
			return "", ErrNoKey
		}
		return v, OK
	case APPEND:

		if v, exist := kv.db[op.Key]; !exist {
			kv.db[op.Key] = op.Value
		} else {
			kv.db[op.Key] = v + op.Value
		}
		break
	}
	return "", OK
}

func (kv *KVServer) servePendingRPC(apply *raft.ApplyMsg, err Err, value string) {
	select {
	case commit := <-kv.committing:
		op, ok := (apply.Command).(Op)
		DPrintf("commitProcess me: %d %+v %+v Index:%d", kv.me, op, commit, apply.CommandIndex)
		commit.wrongLeader = op.SeqId != commit.op.SeqId || op.ClerkId != commit.op.ClerkId || !ok || !apply.CommandValid
		commit.leader = op.ServerId
		commit.err = err
		commit.value = value
		close(commit.done)
	default:
	}

}

func (kv *KVServer) updateClerkTrack(clerkId int64, seqId int) {
	kv.mu.Lock()
	kv.clerkTrack[clerkId] = seqId
	kv.mu.Unlock()
}

func (kv *KVServer) commitProcess() {
	for {
		select {
		case apply := <-kv.applyCh:
			var err Err
			var value string
			if apply.CommandValid {
				op, _ := (apply.Command).(Op)
				value, err = kv.execute(&op)
				kv.updateClerkTrack(op.ClerkId, op.SeqId)
				DPrintf("server%d apply %+v Index:%d", kv.me, op, apply.CommandIndex)
			}
			kv.servePendingRPC(&apply, err, value)
			break
		case <-kv.ctx.Done():
			select {
			case commit := <-kv.committing:
				close(commit.done)
			default:
			}
			return
		}
	}
}

//
// the tester calls Kill() when a KVServer instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (kv *KVServer) Kill() {
	kv.rf.Kill()
	// Your code here, if desired.
	kv.cancel()
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
//
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate

	// You may need initialization code here.
	kv.booting = true
	kv.db = make(map[string]string)
	kv.clerkTrack = make(map[int64]int)
	kv.issueing = make(chan *KVRPCReq)
	kv.committing = make(chan *KVRPCResp, 1)
	kv.ctx, kv.cancel = context.WithCancel(context.Background())

	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh, true)

	// You may need initialization code here.
	go kv.commitProcess()
	go kv.rpcProcess()

	return kv
}
