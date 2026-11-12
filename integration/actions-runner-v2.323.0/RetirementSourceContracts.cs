// Test overlay for exact official actions/runner d8b82f44 only. It adds NO
// production drain implementation, proof constructor, registration or network.
using System;
using System.Collections.Generic;
using System.Reflection;
using System.Runtime.CompilerServices;
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
    public sealed class RetirementSourceContracts
    {
        private const string SourcePin = "d8b82f44a9ec0258904d5869ae683c31d5aac299";
        private static readonly TimeSpan Bound = TimeSpan.FromSeconds(10);

        private static TaskCompletionSource<bool> Signal() => new(TaskCreationOptions.RunContinuationsAsynchronously);

        private static Pipelines.AgentJobRequestMessage Job(string name)
        {
            return new Pipelines.AgentJobRequestMessage(new TaskOrchestrationPlanReference(), null,
                Guid.NewGuid(), name, name, null, null, null,
                new Dictionary<string, VariableValue>(), new List<MaskHint>(), new Pipelines.JobResources(),
                new Pipelines.ContextData.DictionaryContextData(), new Pipelines.WorkspaceOptions(),
                new List<Pipelines.ActionStep>(), null, null, null, null, null);
        }

        private static TaskAgentMessage Message(string name, long id) => new()
        {
            MessageId = id,
            MessageType = JobRequestMessageTypes.PipelineAgentJobRequest,
            Body = JsonUtility.ToString(Job(name)),
        };

        private sealed class ListenerFixture : IDisposable
        {
            public readonly TestHostContext Host;
            public readonly Mock<IMessageListener> Listener = new();
            public readonly Mock<IJobDispatcher> Dispatcher = new();
            public readonly Mock<IConfigurationManager> Configuration = new();
            public readonly TaskCompletionSource<bool> RunOnceCompleted = Signal();
            public readonly GitHub.Runner.Listener.Runner Runner = new();

            public ListenerFixture(object owner, bool ephemeral, [CallerMemberName] string name = "")
            {
                Assert.Equal(SourcePin, BuildConstants.Source.CommitHash);
                Host = new TestHostContext(owner, name);
                var store = new Mock<IConfigurationStore>();
                store.Setup(x => x.IsServiceConfigured()).Returns(false);
                Configuration.Setup(x => x.LoadSettings()).Returns(new RunnerSettings { PoolId = 1, Ephemeral = ephemeral });
                Configuration.Setup(x => x.IsConfigured()).Returns(true);
                Host.SetSingleton<IConfigurationManager>(Configuration.Object);
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
                Dispatcher.SetupGet(x => x.RunOnceJobCompleted).Returns(RunOnceCompleted);
                Dispatcher.Setup(x => x.ShutdownAsync()).Returns(Task.CompletedTask);
                Runner.Initialize(Host);
            }

            public Task<int> Start() => Runner.ExecuteCommand(new CommandSettings(Host, new[] { "run" }));
            public void Dispose() { Host.ShutdownRunner(ShutdownReason.UserCancelled); Host.Dispose(); }
        }

        private static (TestHostContext host, JobDispatcher dispatcher, Mock<IRunnerServer> server)
            Dispatcher(object owner, [CallerMemberName] string name = "")
        {
            Assert.Equal(SourcePin, BuildConstants.Source.CommitHash);
            var host = new TestHostContext(owner, name);
            var settings = new Mock<IConfigurationStore>();
            settings.Setup(x => x.GetSettings()).Returns(new RunnerSettings { PoolId = 1 });
            var server = new Mock<IRunnerServer>();
            host.SetSingleton<IConfigurationStore>(settings.Object);
            host.SetSingleton<IRunnerServer>(server.Object);
            host.SetSingleton<IJobNotification>(new Mock<IJobNotification>().Object);
            var dispatcher = new JobDispatcher();
            dispatcher.Initialize(host);
            return (host, dispatcher, server);
        }

        // Use the ACTUAL private dispatcher record/method, never a parallel model.
        // This reflection is confined to a test overlay for the exact source pin.
        private static object AcceptedWorker(JobDispatcher dispatcher, Task pending, out CancellationToken cancellation)
        {
            var nested = typeof(JobDispatcher).GetNestedType("WorkerDispatcher", BindingFlags.NonPublic);
            Assert.NotNull(nested);
            var id = Guid.NewGuid();
            var worker = Activator.CreateInstance(nested, new object[] { id, 77L });
            nested.GetProperty("WorkerDispatch").SetValue(worker, pending);
            cancellation = ((CancellationTokenSource)nested.GetProperty("WorkerCancellationTokenSource").GetValue(worker)).Token;
            var records = typeof(JobDispatcher).GetField("_jobInfos", BindingFlags.NonPublic | BindingFlags.Instance).GetValue(dispatcher);
            Assert.True((bool)records.GetType().GetMethod("TryAdd").Invoke(records, new[] { (object)id, worker }));
            var queue = (Queue<Guid>)typeof(JobDispatcher).GetField("_jobDispatchedQueue", BindingFlags.NonPublic | BindingFlags.Instance).GetValue(dispatcher);
            queue.Enqueue(id);
            return worker;
        }

        [Fact]
        public async Task ShutdownCancelsAcceptedWorkerBeforeWaitingForItsCompletion()
        {
            var fixture = Dispatcher(this);
            using var host = fixture.host;
            var finish = Signal();
            AcceptedWorker(fixture.dispatcher, finish.Task, out var cancellation);
            var shutdown = fixture.dispatcher.ShutdownAsync();
            try
            {
                Assert.True(cancellation.IsCancellationRequested);
                Assert.False(shutdown.IsCompleted);
            }
            finally { finish.TrySetResult(true); }
            await shutdown.WaitAsync(Bound);
        }

        [Fact]
        public async Task ServerTerminalJobStatusDoesNotMeanWorkerIsQuiescent()
        {
            var fixture = Dispatcher(this);
            using var host = fixture.host;
            var request = new TaskAgentJobRequest();
            typeof(TaskAgentJobRequest).GetProperty("Result").SetValue(request, TaskResult.Succeeded);
            fixture.server.Setup(x => x.GetAgentRequestAsync(1, 77, It.IsAny<CancellationToken>())).ReturnsAsync(request);
            var finish = Signal();
            var worker = AcceptedWorker(fixture.dispatcher, finish.Task, out var cancellation);
            var ensure = typeof(JobDispatcher).GetMethod("EnsureDispatchFinished", BindingFlags.NonPublic | BindingFlags.Instance);
            Assert.NotNull(ensure);
            var pending = (Task)ensure.Invoke(fixture.dispatcher, new[] { worker, (object)false });
            try
            {
                Assert.True(cancellation.IsCancellationRequested);
                Assert.False(pending.IsCompleted);
            }
            finally { finish.TrySetResult(true); }
            await pending.WaitAsync(Bound);
            fixture.server.Verify(x => x.GetAgentRequestAsync(1, 77, It.IsAny<CancellationToken>()), Times.Once);
        }

        [Fact]
        public async Task RunOnceCompletionCanSignalWithoutAnyWorkerStarting()
        {
            var fixture = Dispatcher(this);
            using var host = fixture.host;
            var channel = new Mock<IProcessChannel>();
            host.EnqueueInstance<IProcessChannel>(channel.Object);
            host.EnqueueInstance<IProcessInvoker>(new Mock<IProcessInvoker>().Object);
            fixture.server.Setup(x => x.RenewAgentRequestAsync(It.IsAny<int>(), It.IsAny<long>(), It.IsAny<Guid>(), It.IsAny<string>(), It.IsAny<CancellationToken>()))
                .Returns(Task.FromException<TaskAgentJobRequest>(new TaskAgentJobNotFoundException("disposable no-longer-valid request")));
            fixture.dispatcher.Run(Job("never-started"), true);
            await fixture.dispatcher.WaitAsync(CancellationToken.None).WaitAsync(Bound);
            Assert.True(fixture.dispatcher.RunOnceJobCompleted.Task.IsCompletedSuccessfully);
            Assert.True(await fixture.dispatcher.RunOnceJobCompleted.Task);
            channel.Verify(x => x.StartServer(It.IsAny<StartProcessDelegate>()), Times.Never);
            fixture.server.Verify(x => x.FinishAgentRequestAsync(It.IsAny<int>(), It.IsAny<long>(), It.IsAny<Guid>(), It.IsAny<DateTime>(), It.IsAny<TaskResult>(), It.IsAny<CancellationToken>()), Times.Never);
        }

        [Fact]
        public async Task AlreadyRetrievedMessageCanDispatchAfterShutdownTokenWasCancelled()
        {
            using var fixture = new ListenerFixture(this, false);
            var dispatchedWhileCancelled = false;
            fixture.Listener.Setup(x => x.GetNextMessageAsync(It.IsAny<CancellationToken>())).Returns(() =>
            {
                fixture.Host.ShutdownRunner(ShutdownReason.UserCancelled);
                return Task.FromResult(Message("retrieved-before-stop", 1));
            });
            fixture.Dispatcher.Setup(x => x.Run(It.IsAny<Pipelines.AgentJobRequestMessage>(), false))
                .Callback(() => dispatchedWhileCancelled = fixture.Host.RunnerShutdownToken.IsCancellationRequested);
            await fixture.Start().WaitAsync(Bound);
            Assert.True(dispatchedWhileCancelled);
            fixture.Dispatcher.Verify(x => x.Run(It.IsAny<Pipelines.AgentJobRequestMessage>(), false), Times.Once);
            fixture.Dispatcher.Verify(x => x.ShutdownAsync(), Times.Once);
            fixture.Listener.Verify(x => x.DeleteSessionAsync(), Times.Once);
        }

        [Fact]
        public async Task EphemeralSkipsSecondDispatchButDoesNotProveNoSecondAssignment()
        {
            using var fixture = new ListenerFixture(this, true);
            var first = Message("first", 1);
            var second = Message("already-assigned-second", 2);
            var thirdPoll = Signal();
            var polls = 0;
            fixture.Listener.Setup(x => x.GetNextMessageAsync(It.IsAny<CancellationToken>())).Returns(async (CancellationToken token) =>
            {
                var count = Interlocked.Increment(ref polls);
                if (count == 1) return first;
                if (count == 2) return second;
                thirdPoll.TrySetResult(true);
                await Task.Delay(Timeout.Infinite, token);
                return null;
            });
            var run = fixture.Start();
            try
            {
                await thirdPoll.Task.WaitAsync(Bound);
                fixture.Dispatcher.Verify(x => x.Run(It.IsAny<Pipelines.AgentJobRequestMessage>(), true), Times.Once);
                fixture.Listener.Verify(x => x.DeleteMessageAsync(second), Times.Never);
                Assert.False(run.IsCompleted);
            }
            finally { fixture.RunOnceCompleted.TrySetResult(true); }
            Assert.Equal(Constants.Runner.ReturnCode.Success, await run.WaitAsync(Bound));
            fixture.Listener.Verify(x => x.DeleteMessageAsync(first), Times.Once);
        }

        [Fact]
        public async Task RunOnceSignalDoesNotSkipShutdownCleanupBeforeSessionDeletion()
        {
            using var fixture = new ListenerFixture(this, true);
            var shutdownEntered = Signal();
            var finishCleanup = Signal();
            var polls = 0;
            fixture.Listener.Setup(x => x.GetNextMessageAsync(It.IsAny<CancellationToken>())).Returns(async (CancellationToken token) =>
            {
                if (Interlocked.Increment(ref polls) == 1) return Message("one-job", 1);
                await Task.Delay(Timeout.Infinite, token);
                return null;
            });
            fixture.Dispatcher.Setup(x => x.Run(It.IsAny<Pipelines.AgentJobRequestMessage>(), true))
                .Callback(() => fixture.RunOnceCompleted.TrySetResult(true));
            fixture.Dispatcher.Setup(x => x.ShutdownAsync()).Returns(() =>
            {
                shutdownEntered.TrySetResult(true);
                return finishCleanup.Task;
            });
            var run = fixture.Start();
            try
            {
                await shutdownEntered.Task.WaitAsync(Bound);
                Assert.True(fixture.RunOnceCompleted.Task.IsCompleted);
                Assert.False(run.IsCompleted);
                fixture.Listener.Verify(x => x.DeleteSessionAsync(), Times.Never);
            }
            finally { finishCleanup.TrySetResult(true); }
            Assert.Equal(Constants.Runner.ReturnCode.Success, await run.WaitAsync(Bound));
            fixture.Listener.Verify(x => x.DeleteSessionAsync(), Times.Once);
            fixture.Configuration.Verify(x => x.DeleteLocalRunnerConfig(), Times.Once);
        }
    }
}
