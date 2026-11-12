// Test-only local protocol experiment; no production implementation or permission.
using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using GitHub.DistributedTask.WebApi;
using GitHub.Runner.Listener;
using GitHub.Runner.Listener.Configuration;
using GitHub.Runner.Sdk;
using GitHub.Services.WebApi;
using Moq;
using Xunit;
using Pipelines = GitHub.DistributedTask.Pipelines;

namespace GitHub.Runner.Common.Tests.Listener
{
    public sealed class RetirementSourceContractsLocalIntake
    {
        private static readonly TimeSpan Bound = TimeSpan.FromSeconds(10);
        private static TaskCompletionSource<bool> Signal() => new(TaskCreationOptions.RunContinuationsAsynchronously);
        private static TaskAgentMessage Message(long id)
        {
            var job = new Pipelines.AgentJobRequestMessage(new TaskOrchestrationPlanReference(), null,
                Guid.NewGuid(), "local-experiment", "local-experiment", null, null, null,
                new Dictionary<string, VariableValue>(), new List<MaskHint>(), new Pipelines.JobResources(),
                new Pipelines.ContextData.DictionaryContextData(), new Pipelines.WorkspaceOptions(),
                new List<Pipelines.ActionStep>(), null, null, null, null, null);
            return new TaskAgentMessage { MessageId = id, MessageType = JobRequestMessageTypes.PipelineAgentJobRequest, Body = JsonUtility.ToString(job) };
        }
        private static Guid JobId(TaskAgentMessage message) => JsonUtility.FromString<Pipelines.AgentJobRequestMessage>(message.Body).JobId;
        private sealed class ActualRunnerFixture : IDisposable
        {
            public readonly TestHostContext Host;
            public readonly Mock<IMessageListener> Listener = new();
            public readonly Mock<IJobDispatcher> Dispatcher = new();
            public readonly TaskCompletionSource<bool> RunOnce = Signal();
            public readonly TaskCompletionSource<bool> ShutdownEntered = Signal();
            public readonly TaskCompletionSource<bool> ShutdownPassedLocalGate = Signal();
            public readonly LocalIntakeFenceExperiment Gate = new();
            private readonly GitHub.Runner.Listener.Runner runner = new();
            public ActualRunnerFixture(object owner, string name)
            {
                Assert.Equal("d8b82f44a9ec0258904d5869ae683c31d5aac299", BuildConstants.Source.CommitHash);
                Host = new TestHostContext(owner, name);
                var configuration = new Mock<IConfigurationManager>();
                var store = new Mock<IConfigurationStore>();
                configuration.Setup(x => x.LoadSettings()).Returns(new RunnerSettings { PoolId = 1, Ephemeral = true });
                configuration.Setup(x => x.IsConfigured()).Returns(true);
                store.Setup(x => x.IsServiceConfigured()).Returns(false);
                Host.SetSingleton<IConfigurationManager>(configuration.Object);
                Host.SetSingleton<IConfigurationStore>(store.Object);
                Host.SetSingleton<IMessageListener>(Listener.Object);
                Host.SetSingleton<IJobNotification>(new Mock<IJobNotification>().Object);
                Host.SetSingleton<IPromptManager>(new Mock<IPromptManager>().Object);
                Host.SetSingleton<IRunnerServer>(new Mock<IRunnerServer>().Object);
                Host.EnqueueInstance<IErrorThrottler>(new Mock<IErrorThrottler>().Object);
                Host.EnqueueInstance<IJobDispatcher>(Dispatcher.Object);
                Listener.Setup(x => x.CreateSessionAsync(It.IsAny<CancellationToken>())).ReturnsAsync(CreateSessionResult.Success);
                Listener.Setup(x => x.DeleteSessionAsync()).Returns(Task.CompletedTask);
                Listener.Setup(x => x.DeleteMessageAsync(It.IsAny<TaskAgentMessage>())).Returns(Task.CompletedTask);
                Dispatcher.SetupGet(x => x.RunOnceJobCompleted).Returns(RunOnce);
                // This is a MOCK shutdown boundary, not a patch to real dispatcher
                // cancellation. The six original tests still execute that real code.
                Dispatcher.Setup(x => x.ShutdownAsync()).Returns(async () =>
                {
                    ShutdownEntered.TrySetResult(true);
                    await Gate.WaitForLocalQuiescenceAsync(CancellationToken.None);
                    ShutdownPassedLocalGate.TrySetResult(true);
                });
                runner.Initialize(Host);
            }
            public Task<int> Start(Func<CancellationToken, Task<TaskAgentMessage>> upstream)
            {
                Listener.Setup(x => x.GetNextMessageAsync(It.IsAny<CancellationToken>()))
                    .Returns((CancellationToken token) => Gate.PollAsync(upstream, token));
                return runner.ExecuteCommand(new CommandSettings(Host, new[] { "run" }));
            }
            public void Dispose() { Host.ShutdownRunner(ShutdownReason.UserCancelled); Host.Dispose(); }
        }

        [Fact]
        public async Task LocalSealKeepsLatePreSealAssignmentAndCleanupAsObligations()
        {
            using var fixture = new ActualRunnerFixture(this, nameof(LocalSealKeepsLatePreSealAssignmentAndCleanupAsObligations));
            var entered = Signal(); var acquired = new TaskCompletionSource<TaskAgentMessage>(TaskCreationOptions.RunContinuationsAsynchronously);
            var dispatched = Signal(); var worker = Signal(); var cleanup = Signal();
            fixture.Dispatcher.Setup(x => x.Run(It.IsAny<Pipelines.AgentJobRequestMessage>(), true)).Callback<Pipelines.AgentJobRequestMessage, bool>((job, _) =>
            {
                fixture.Gate.BindWork(job.JobId, worker.Task, cleanup.Task); dispatched.TrySetResult(true);
            });
            var polls = 0;
            var run = fixture.Start(_ => { Interlocked.Increment(ref polls); entered.TrySetResult(true); return acquired.Task; });
            await entered.Task.WaitAsync(Bound);
            fixture.Gate.Seal();
            Assert.Equal(1, fixture.Gate.Snapshot().Polls);
            Assert.False(fixture.Gate.Snapshot().LocallyQuiescent);
            acquired.SetResult(Message(1));
            await dispatched.Task.WaitAsync(Bound);
            Assert.Equal(1, fixture.Gate.Snapshot().Outstanding);
            Assert.False(fixture.Host.RunnerShutdownToken.IsCancellationRequested);
            worker.SetResult(true);
            Assert.False(fixture.Gate.Snapshot().LocallyQuiescent);
            cleanup.SetResult(true);
            await fixture.Gate.WaitForLocalQuiescenceAsync(CancellationToken.None).WaitAsync(Bound);
            Assert.Equal(1, polls);
            fixture.RunOnce.SetResult(true);
            Assert.Equal(Constants.Runner.ReturnCode.Success, await run.WaitAsync(Bound));
        }

        [Fact]
        public async Task LocalObserverDoesNotPromoteRunOnceOrInterruptAcceptedWork()
        {
            using var fixture = new ActualRunnerFixture(this, nameof(LocalObserverDoesNotPromoteRunOnceOrInterruptAcceptedWork));
            var dispatched = Signal(); var worker = Signal(); var cleanup = Signal();
            fixture.Dispatcher.Setup(x => x.Run(It.IsAny<Pipelines.AgentJobRequestMessage>(), true)).Callback<Pipelines.AgentJobRequestMessage, bool>((job, _) =>
            {
                fixture.Gate.BindWork(job.JobId, worker.Task, cleanup.Task); fixture.Gate.Seal(); dispatched.TrySetResult(true);
                fixture.RunOnce.TrySetResult(true); // adversarial premature completion signal
            });
            var run = fixture.Start(_ => Task.FromResult(Message(1)));
            await dispatched.Task.WaitAsync(Bound);
            await fixture.ShutdownEntered.Task.WaitAsync(Bound);
            Assert.False(fixture.ShutdownPassedLocalGate.Task.IsCompleted);
            fixture.Listener.Verify(x => x.DeleteSessionAsync(), Times.Never);
            using var observation = new CancellationTokenSource();
            var waiting = fixture.Gate.WaitForLocalQuiescenceAsync(observation.Token);
            observation.Cancel();
            await Assert.ThrowsAnyAsync<OperationCanceledException>(() => waiting);
            Assert.False(fixture.Host.RunnerShutdownToken.IsCancellationRequested);
            Assert.False(worker.Task.IsCompleted);
            worker.SetResult(true);
            Assert.False(fixture.ShutdownPassedLocalGate.Task.IsCompleted);
            cleanup.SetResult(true);
            Assert.Equal(Constants.Runner.ReturnCode.Success, await run.WaitAsync(Bound));
            Assert.True(fixture.ShutdownPassedLocalGate.Task.IsCompletedSuccessfully);
            fixture.Listener.Verify(x => x.DeleteSessionAsync(), Times.Once);
        }

        [Fact]
        public async Task LocalPollSealAlsoSuppressesControlTrafficSoIsNotAQualifiedDrain()
        {
            using var fixture = new ActualRunnerFixture(this, nameof(LocalPollSealAlsoSuppressesControlTrafficSoIsNotAQualifiedDrain));
            var dispatched = Signal(); var worker = Signal(); var cleanup = Signal();
            fixture.Dispatcher.Setup(x => x.Run(It.IsAny<Pipelines.AgentJobRequestMessage>(), true)).Callback<Pipelines.AgentJobRequestMessage, bool>((job, _) =>
            {
                fixture.Gate.BindWork(job.JobId, worker.Task, cleanup.Task); fixture.Gate.Seal(); dispatched.TrySetResult(true);
            });
            var first = Message(1);
            var ownerCancellation = new JobCancelMessage(JobId(first), TimeSpan.FromMinutes(5)).GetAgentMessage();
            ownerCancellation.MessageId = 2;
            var polls = 0; var ownerCancellationDelivered = false;
            var run = fixture.Start(_ =>
            {
                if (Interlocked.Increment(ref polls) == 1) return Task.FromResult(first);
                ownerCancellationDelivered = true; // would arrive only through the same message stream
                return Task.FromResult(ownerCancellation);
            });
            await dispatched.Task.WaitAsync(Bound);
            Assert.False(ownerCancellationDelivered);
            Assert.Equal(1, polls);
            Assert.False(fixture.Gate.Snapshot().LocallyQuiescent);
            // No claim that the local gate preserves owner cancellation delivery,
            // renewal/broker semantics, restart exclusion or remote assignment.
            worker.SetResult(true); cleanup.SetResult(true); fixture.RunOnce.SetResult(true);
            Assert.Equal(Constants.Runner.ReturnCode.Success, await run.WaitAsync(Bound));
            Assert.False(ownerCancellationDelivered);
            fixture.Dispatcher.Verify(x => x.Cancel(It.IsAny<JobCancelMessage>()), Times.Never);
        }

        [Fact]
        public async Task FailedOrCancelledAcquisitionCannotBecomeNoAssignmentProof()
        {
            foreach (var cancel in new[] { false, true })
            {
                var gate = new LocalIntakeFenceExperiment();
                var failed = new TaskCompletionSource<TaskAgentMessage>(TaskCreationOptions.RunContinuationsAsynchronously);
                var poll = gate.PollAsync(_ => failed.Task, CancellationToken.None);
                gate.Seal();
                if (cancel) failed.SetCanceled(); else failed.SetException(new InvalidOperationException("ambiguous remote acceptance"));
                await Assert.ThrowsAnyAsync<Exception>(() => poll);
                Assert.True(gate.Snapshot().Ambiguous);
                Assert.False(gate.Snapshot().LocallyQuiescent);
                await Assert.ThrowsAsync<InvalidOperationException>(() => gate.WaitForLocalQuiescenceAsync(CancellationToken.None));
            }
            foreach (var duplicate in new[] { false, true })
            {
                var gate = new LocalIntakeFenceExperiment(); var first = Message(1);
                await gate.PollAsync(_ => Task.FromResult(first), CancellationToken.None);
                var response = duplicate ? first : new JobCancelMessage(JobId(first), TimeSpan.FromMinutes(5)).GetAgentMessage();
                response.MessageId = duplicate ? 1 : 2;
                await Assert.ThrowsAsync<InvalidOperationException>(() => gate.PollAsync(_ => Task.FromResult(response), CancellationToken.None));
                gate.Seal();
                Assert.True(gate.Snapshot().Ambiguous);
                Assert.False(gate.Snapshot().LocallyQuiescent);
            }
        }

        [Fact]
        public async Task AcceptedButNeverDispatchedOrFailedCleanupRemainsUnresolved()
        {
            var gate = new LocalIntakeFenceExperiment(); var message = Message(1);
            await gate.PollAsync(_ => Task.FromResult(message), CancellationToken.None);
            gate.Seal();
            Assert.False(gate.Snapshot().LocallyQuiescent); // no dispatcher/RunOnce status can erase acceptance
            gate.BindWork(JobId(message), Task.CompletedTask, Task.FromException(new InvalidOperationException("cleanup unknown")));
            Assert.True(gate.Snapshot().Ambiguous);
            await Assert.ThrowsAsync<InvalidOperationException>(() => gate.WaitForLocalQuiescenceAsync(CancellationToken.None));
            var previous = new LocalIntakeFenceExperiment(); var accepted = Message(1);
            await previous.PollAsync(_ => Task.FromResult(accepted), CancellationToken.None);
            var worker = Signal(); var cleanup = Signal();
            previous.BindWork(JobId(accepted), worker.Task, cleanup.Task); previous.Seal();
            Assert.False(previous.Snapshot().Ambiguous);
            previous.InvalidateGeneration();
            worker.SetResult(true); cleanup.SetResult(true);
            var fresh = new LocalIntakeFenceExperiment();
            Assert.NotEqual(previous.Generation, fresh.Generation);
            Assert.False(fresh.Snapshot().LocallyQuiescent);
            fresh.Seal();
            Assert.True(fresh.Snapshot().LocallyQuiescent);
            // Deliberate COUNTEREXAMPLE: a fresh empty in-memory object can look
            // locally quiet while old work/history is unresolved. Guid generation
            // changes are NOT a restart/durable-history fence or retirement proof.
            Assert.True(previous.Snapshot().Ambiguous);
            Assert.False(previous.Snapshot().LocallyQuiescent);
            await Assert.ThrowsAsync<InvalidOperationException>(() => previous.WaitForLocalQuiescenceAsync(CancellationToken.None));
        }

        [Fact]
        public async Task PollResolutionAtomicallyTransfersObligationWithoutQuiescentGap()
        {
            for (var iteration = 0; iteration < 100; iteration++)
            {
                var gate = new LocalIntakeFenceExperiment();
                var response = new TaskCompletionSource<TaskAgentMessage>(TaskCreationOptions.RunContinuationsAsynchronously);
                var poll = gate.PollAsync(_ => response.Task, CancellationToken.None);
                gate.Seal();
                var resolve = Task.Run(() => response.SetResult(Message(1)));
                for (var observation = 0; observation < 100; observation++)
                {
                    var current = gate.Snapshot();
                    Assert.False(current.LocallyQuiescent);
                    Assert.True(current.Polls != 0 || current.Outstanding != 0);
                }
                await resolve.WaitAsync(Bound);
                var message = await poll.WaitAsync(Bound);
                gate.BindWork(JobId(message), Task.CompletedTask, Task.CompletedTask);
                await gate.WaitForLocalQuiescenceAsync(CancellationToken.None).WaitAsync(Bound);
            }
        }
    }
}
