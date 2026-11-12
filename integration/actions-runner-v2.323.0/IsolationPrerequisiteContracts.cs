// Exact source prerequisite/counterexample tests. NO container, Docker daemon,
// privileged namespace, supervisor enrollment or isolation proof is created.
using System;
using System.Collections.Generic;
using System.IO;
using GitHub.DistributedTask.WebApi;
using GitHub.Runner.Common;
using GitHub.Runner.Sdk;
using GitHub.Runner.Worker;
using GitHub.Runner.Worker.Container;
using GitHub.Runner.Worker.Container.ContainerHooks;
using Xunit;
using Pipelines = GitHub.DistributedTask.Pipelines;

namespace GitHub.Runner.Common.Tests.Listener
{
    public sealed class RetirementSourceContractsIsolationPrerequisites
    {
        private TestHostContext Host(string name)
        {
            Assert.Equal("d8b82f44a9ec0258904d5869ae683c31d5aac299", BuildConstants.Source.CommitHash);
            return new TestHostContext(this, name);
        }
        [Fact]
        public void DefaultLinuxJobContainerIncludesHostDockerSocket()
        {
            using var host = Host(nameof(DefaultLinuxJobContainerIncludesHostDockerSocket));
            var container = new ContainerInfo(host, new Pipelines.JobContainer { Image = "registry.invalid/fixture:unused" });
            Assert.Contains(container.SystemMountVolumes, volume =>
                volume.SourceVolumePath == "/var/run/docker.sock" && volume.TargetVolumePath == "/var/run/docker.sock" && !volume.ReadOnly);
            // This merely constructs the ACTUAL defaults. No Docker call occurs.
        }
        [Fact]
        public void JobContainerHookInputRetainsPrivilegedOptionsAndHostMounts()
        {
            using var host = Host(nameof(JobContainerHookInputRetainsPrivilegedOptionsAndHostMounts));
            var definition = new Pipelines.JobContainer { Image = "registry.invalid/fixture:unused",
                Options = "--privileged --pid=host --network=host", Volumes = new List<string> { "/:/host" } };
            var container = new ContainerInfo(host, definition);
            var hook = new HookContainer(container);
            Assert.Equal(definition.Options, hook.CreateOptions);
            Assert.Contains(hook.UserMountVolumes, volume => volume.SourceVolumePath == "/" && volume.TargetVolumePath == "/host" && !volume.ReadOnly);
            Assert.Contains(hook.SystemMountVolumes, volume => volume.SourceVolumePath == "/var/run/docker.sock");
            // 'System' mount naming and hook serialization are NOT security policy.
        }
        [Fact]
        public void HookPathAloneDoesNotEnableHooksWithoutServiceFeatureFlag()
        {
            using var host = Host(nameof(HookPathAloneDoesNotEnableHooksWithoutServiceFeatureFlag));
            var previous = Environment.GetEnvironmentVariable(Constants.Hooks.ContainerHooksPath);
            try
            {
                Environment.SetEnvironmentVariable(Constants.Hooks.ContainerHooksPath, "/nonexistent-test-only-hook.js");
                var disabled = new Variables(host, new Dictionary<string, VariableValue>());
                Assert.False(FeatureManager.IsContainerHooksEnabled(disabled));
                var enabled = new Variables(host, new Dictionary<string, VariableValue> {
                    [Constants.Runner.Features.AllowRunnerContainerHooks] = new VariableValue("true", false) });
                Assert.True(FeatureManager.IsContainerHooksEnabled(enabled));
                // TRUE selects a code path only. It is not executable validation,
                // job confinement, protected credentials or supervisor authority.
            }
            finally { Environment.SetEnvironmentVariable(Constants.Hooks.ContainerHooksPath, previous); }
        }
        [Fact]
        public void JavaScriptHookSelectsAbsoluteBundledNodeNotJobPrependPath()
        {
            using var host = Host(nameof(JavaScriptHookSelectsAbsoluteBundledNodeNotJobPrependPath));
            var shell = host.GetDefaultShellForScript("/nonexistent-test-only-hook.js", "/untrusted-job-prepend-path");
            Assert.StartsWith(host.GetDirectory(WellKnownDirectory.Externals) + Path.DirectorySeparatorChar, shell);
            Assert.Contains(Path.Combine("bin", "node"), shell);
            Assert.EndsWith(" {0}", shell);
            Assert.DoesNotContain("/untrusted-job-prepend-path", shell);
            // This is an interpreter-selection property only. Image ownership,
            // environment, hook code and child execution still require qualification.
        }
    }
}
