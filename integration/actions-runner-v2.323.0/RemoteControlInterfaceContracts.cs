// Exact pinned client behavior with mocked transports. Not a server contract,
// live GitHub experiment, assignment seal, restart fence or retirement permit.
using System;
using System.Threading;
using System.Threading.Tasks;
using GitHub.DistributedTask.WebApi;
using GitHub.Runner.Listener;
using GitHub.Runner.Listener.Configuration;
using GitHub.Services.Common;
using GitHub.Services.WebApi;
using Moq;
using Xunit;

namespace GitHub.Runner.Common.Tests.Listener
{
    public sealed class RetirementSourceContractsRemoteControl
    {
        private static readonly TimeSpan Bound = TimeSpan.FromSeconds(10);
        private static TaskAgentMessage Job(long id) => new() { MessageId = id, MessageType = JobRequestMessageTypes.PipelineAgentJobRequest, Body = "{}" };
        private static TaskAgentMessage Control(long id)
        {
            var message = new JobCancelMessage(Guid.NewGuid(), TimeSpan.FromMinutes(5)).GetAgentMessage();
            message.MessageId = id; return message;
        }
        private sealed class Fixture : IDisposable
        {
            public readonly TestHostContext Host;
            public readonly Mock<IRunnerServer> Server = new();
            public readonly Mock<IBrokerServer> Broker = new();
            public readonly RunnerSettings Settings;
            public Fixture(object owner, string name, bool skipRecovery = true)
            {
                Assert.Equal("d8b82f44a9ec0258904d5869ae683c31d5aac299", BuildConstants.Source.CommitHash);
                Host = new TestHostContext(owner, name);
                Settings = new RunnerSettings { AgentId = 1, AgentName = "source-fixture", PoolId = 1,
                    ServerUrl = "https://runner.invalid/", ServerUrlV2 = "https://broker.invalid/", SkipSessionRecover = skipRecovery };
                var configuration = new Mock<IConfigurationManager>();
                configuration.Setup(x => x.LoadSettings()).Returns(Settings);
                var credentials = new Mock<ICredentialManager>();
                credentials.Setup(x => x.LoadCredentials()).Returns(new VssCredentials());
                Host.SetSingleton<IConfigurationManager>(configuration.Object);
                Host.SetSingleton<ICredentialManager>(credentials.Object);
                Host.SetSingleton<IConfigurationStore>(new Mock<IConfigurationStore>().Object);
                Host.SetSingleton<IRunnerServer>(Server.Object);
                Host.SetSingleton<IBrokerServer>(Broker.Object);
                Server.Setup(x => x.CreateAgentSessionAsync(1, It.IsAny<TaskAgentSession>(), It.IsAny<CancellationToken>()))
                    .Returns(() => Task.FromResult(Session()));
                Broker.Setup(x => x.CreateSessionAsync(It.IsAny<TaskAgentSession>(), It.IsAny<CancellationToken>()))
                    .Returns(() => Task.FromResult(Session()));
            }
            private static TaskAgentSession Session()
            {
                var session = new TaskAgentSession();
                typeof(TaskAgentSession).GetProperty("SessionId").SetValue(session, Guid.NewGuid());
                return session;
            }
            public async Task<MessageListener> Classic()
            {
                var listener = new MessageListener(); listener.Initialize(Host);
                Assert.Equal(CreateSessionResult.Success, await listener.CreateSessionAsync(CancellationToken.None).WaitAsync(Bound));
                return listener;
            }
            public async Task<BrokerMessageListener> BrokerListener()
            {
                var listener = new BrokerMessageListener(); listener.Initialize(Host);
                Assert.Equal(CreateSessionResult.Success, await listener.CreateSessionAsync(CancellationToken.None).WaitAsync(Bound));
                return listener;
            }
            public void Dispose() => Host.Dispose();
        }

        [Fact]
        public async Task ClassicBusyHintPreservesSharedControlPollingButIsNotAnAssignmentSeal()
        {
            using var fixture = new Fixture(this, nameof(ClassicBusyHintPreservesSharedControlPollingButIsNotAnAssignmentSeal));
            var control = Control(1); var oldJob = Job(2); var polls = 0;
            fixture.Server.Setup(x => x.GetAgentMessageAsync(1, It.IsAny<Guid>(), It.IsAny<long?>(), TaskAgentStatus.Busy,
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(), It.IsAny<bool>(), It.IsAny<CancellationToken>()))
                .Returns(() => Task.FromResult(++polls == 1 ? control : oldJob));
            var listener = await fixture.Classic();
            listener.OnJobStatus(this, new JobStatusEventArgs(TaskAgentStatus.Busy));
            Assert.Same(control, await listener.GetNextMessageAsync(CancellationToken.None).WaitAsync(Bound));
            Assert.Same(oldJob, await listener.GetNextMessageAsync(CancellationToken.None).WaitAsync(Bound));
            Assert.Equal(2, polls);
            // Transport counterexample only: Busy is a request parameter, no typed
            // server seal/frontier is returned or checked by this client method.
        }

        [Fact]
        public async Task ClassicStatusCancellationCanStillReturnTheOldPollJobResponse()
        {
            using var fixture = new Fixture(this, nameof(ClassicStatusCancellationCanStillReturnTheOldPollJobResponse));
            var response = new TaskCompletionSource<TaskAgentMessage>(TaskCreationOptions.RunContinuationsAsynchronously);
            CancellationToken inFlight = default;
            fixture.Server.Setup(x => x.GetAgentMessageAsync(1, It.IsAny<Guid>(), It.IsAny<long?>(), TaskAgentStatus.Online,
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(), It.IsAny<bool>(), It.IsAny<CancellationToken>()))
                .Returns((int pool, Guid session, long? last, TaskAgentStatus status, string version, string os, string arch, bool updates, CancellationToken token) =>
                { inFlight = token; return response.Task; });
            var listener = await fixture.Classic();
            var poll = listener.GetNextMessageAsync(CancellationToken.None);
            Assert.True(inFlight.CanBeCanceled);
            listener.OnJobStatus(this, new JobStatusEventArgs(TaskAgentStatus.Busy));
            Assert.True(inFlight.IsCancellationRequested);
            var accepted = Job(1); response.SetResult(accepted); // remote response wins cancellation
            Assert.Same(accepted, await poll.WaitAsync(Bound));
        }

        [Fact]
        public async Task ClassicSessionExpiryRecreatesUnlessSkipSessionRecoverIsSet()
        {
            foreach (var skip in new[] { false, true })
            {
                using var fixture = new Fixture(this, nameof(ClassicSessionExpiryRecreatesUnlessSkipSessionRecoverIsSet) + skip, skip);
                var expected = Control(1); var polls = 0;
                fixture.Server.Setup(x => x.GetAgentMessageAsync(1, It.IsAny<Guid>(), It.IsAny<long?>(), It.IsAny<TaskAgentStatus>(),
                    It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(), It.IsAny<bool>(), It.IsAny<CancellationToken>()))
                    .Returns(() => ++polls == 1
                        ? Task.FromException<TaskAgentMessage>(new TaskAgentSessionExpiredException("disposable expired session"))
                        : Task.FromResult(expected));
                var listener = await fixture.Classic();
                if (skip)
                    await Assert.ThrowsAsync<TaskAgentSessionExpiredException>(() => listener.GetNextMessageAsync(CancellationToken.None).WaitAsync(Bound));
                else
                    Assert.Same(expected, await listener.GetNextMessageAsync(CancellationToken.None).WaitAsync(Bound));
                fixture.Server.Verify(x => x.CreateAgentSessionAsync(1, It.IsAny<TaskAgentSession>(), It.IsAny<CancellationToken>()), Times.Exactly(skip ? 1 : 2));
                Assert.Equal(skip ? 1 : 2, polls);
            }
        }

        [Fact]
        public async Task BrokerBusyHintAlsoReturnsJobAndControlWithoutSealAcknowledgment()
        {
            using var fixture = new Fixture(this, nameof(BrokerBusyHintAlsoReturnsJobAndControlWithoutSealAcknowledgment));
            var accepted = Job(1); var control = Control(2); var polls = 0;
            fixture.Broker.Setup(x => x.GetRunnerMessageAsync(It.IsAny<Guid?>(), TaskAgentStatus.Busy,
                It.IsAny<string>(), It.IsAny<string>(), It.IsAny<string>(), It.IsAny<bool>(), It.IsAny<CancellationToken>()))
                .Returns(() => Task.FromResult(++polls == 1 ? accepted : control));
            var listener = await fixture.BrokerListener();
            listener.OnJobStatus(this, new JobStatusEventArgs(TaskAgentStatus.Busy));
            Assert.Same(accepted, await listener.GetNextMessageAsync(CancellationToken.None).WaitAsync(Bound));
            Assert.Same(control, await listener.GetNextMessageAsync(CancellationToken.None).WaitAsync(Bound));
            Assert.Equal(2, polls);
        }
    }
}
