// TEST-ONLY necessary local coordination experiment. Not a runner drain feature,
// supervisor attestation, service-side assignment fence, or retirement permit.
// No runtime assembly or controller imports this file outside disposable tests.
using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using GitHub.DistributedTask.WebApi;
using GitHub.Runner.Common;
using GitHub.Runner.Sdk;
using GitHub.Services.WebApi;
using Pipelines = GitHub.DistributedTask.Pipelines;

namespace GitHub.Runner.Common.Tests.Listener
{
    internal sealed class LocalIntakeFenceExperiment
    {
        private sealed class Work
        {
            public Task Worker;
            public Task Cleanup;
        }
        private readonly object sync = new();
        private readonly HashSet<long> polls = new();
        private readonly HashSet<long> messages = new();
        private readonly Dictionary<Guid, Work> accepted = new();
        private TaskCompletionSource<bool> changed = Signal();
        private long nextPoll;
        private bool sealedIntake;
        private bool ambiguous;
        public Guid Generation { get; } = Guid.NewGuid();
        private static TaskCompletionSource<bool> Signal() => new(TaskCreationOptions.RunContinuationsAsynchronously);
        private void Notify() { var before = changed; changed = Signal(); before.TrySetResult(true); }

        public (bool Sealed, int Polls, int Outstanding, bool Ambiguous, bool LocallyQuiescent) Snapshot()
        {
            lock (sync) return SnapshotLocked();
        }
        private (bool Sealed, int Polls, int Outstanding, bool Ambiguous, bool LocallyQuiescent) SnapshotLocked()
        {
            var outstanding = accepted.Values.Count(w => w.Worker == null || w.Cleanup == null ||
                !w.Worker.IsCompletedSuccessfully || !w.Cleanup.IsCompletedSuccessfully);
            var unknown = ambiguous || accepted.Values.Any(w =>
                (w.Worker != null && (w.Worker.IsFaulted || w.Worker.IsCanceled)) ||
                (w.Cleanup != null && (w.Cleanup.IsFaulted || w.Cleanup.IsCanceled)));
            return (sealedIntake, polls.Count, outstanding, unknown,
                sealedIntake && !unknown && polls.Count == 0 && outstanding == 0);
        }

        // Serializes only NEW local acquisition calls. A pre-seal call is NOT
        // cancelled: its late response must transfer to accepted work atomically.
        // IMPORTANT: Runner's real message stream also carries cancellation/control
        // traffic. Suppressing it is NOT a qualified production drain protocol.
        public async Task<TaskAgentMessage> PollAsync(Func<CancellationToken, Task<TaskAgentMessage>> upstream, CancellationToken token)
        {
            long poll;
            lock (sync)
            {
                if (ambiguous) throw new InvalidOperationException("unresolved local history");
                poll = sealedIntake ? 0 : ++nextPoll;
                if (poll != 0) { polls.Add(poll); Notify(); }
            }
            if (poll == 0)
            {
                await Task.Delay(Timeout.Infinite, token); // local wait, no upstream acquisition
                return null;
            }
            try
            {
                var message = await upstream(token);
                lock (sync)
                {
                    // This experiment covers ONLY this exact classic pipeline-job
                    // path. Unknown/broker/control paths cannot produce quiescence.
                    if (message == null || message.MessageId <= 0 || message.MessageType != JobRequestMessageTypes.PipelineAgentJobRequest)
                        throw new InvalidOperationException("unqualified message path");
                    var job = JsonUtility.FromString<Pipelines.AgentJobRequestMessage>(message.Body);
                    if (job == null || job.JobId == Guid.Empty || !messages.Add(message.MessageId) || accepted.ContainsKey(job.JobId))
                        throw new InvalidOperationException("ambiguous accepted work");
                    accepted.Add(job.JobId, new Work());
                    polls.Remove(poll); // no observable zero-poll/zero-work gap
                    Notify();
                }
                return message;
            }
            catch
            {
                lock (sync) { polls.Remove(poll); ambiguous = true; Notify(); }
                throw; // cancellation/error is NOT evidence that no assignment happened
            }
        }
        public void Seal()
        {
            lock (sync) { sealedIntake = true; Notify(); } // irreversible for this object
        }
        // In tests these tasks are explicit doubles. A real integration would need
        // an independent native worker/descendant/post-job supervisor contract.
        // RunOnce, server terminal status and listener exit are deliberately NOT inputs.
        public void BindWork(Guid job, Task worker, Task cleanup)
        {
            lock (sync)
            {
                if (!accepted.TryGetValue(job, out var work) || work.Worker != null || worker == null || cleanup == null)
                {
                    ambiguous = true; Notify();
                    throw new InvalidOperationException("unknown or rebound work");
                }
                work.Worker = worker; work.Cleanup = cleanup;
                _ = worker.ContinueWith(_ => { lock (sync) Notify(); }, TaskScheduler.Default);
                _ = cleanup.ContinueWith(_ => { lock (sync) Notify(); }, TaskScheduler.Default);
                Notify();
            }
        }
        public void InvalidateGeneration()
        {
            lock (sync) { ambiguous = true; Notify(); }
        }
        public async Task WaitForLocalQuiescenceAsync(CancellationToken observationCancellation)
        {
            while (true)
            {
                Task notification;
                lock (sync)
                {
                    var current = SnapshotLocked();
                    if (current.Ambiguous) throw new InvalidOperationException("local history is not qualifying evidence");
                    if (current.LocallyQuiescent) return;
                    notification = changed.Task;
                }
                // Cancels only this observation, NEVER upstream polls or worker tasks.
                await notification.WaitAsync(observationCancellation);
            }
        }
    }
}
