import { test as base } from "@playwright/test";
import { readFile } from "node:fs/promises";

// Chromium's credentialless fetch path does not consistently inherit the
// Playwright context's ignoreHTTPSErrors. Trust exactly this run's disposable
// server public key, never all certificates, and leave application TLS intact.
export const test = base.extend({
  launchOptions: [
    async ({}, use) => {
      const spki = (
        await readFile(new URL("../.admin-test-spki", import.meta.url), "utf8")
      ).trim();
      if (!/^[A-Za-z0-9+/]{43}=$/.test(spki)) {
        throw new Error(
          "Disposable TLS fixture did not supply a SHA256 SPKI fingerprint",
        );
      }
      await use({
        args: [`--ignore-certificate-errors-spki-list=${spki}`],
        ...(process.env.CHROMIUM_PATH
          ? { executablePath: process.env.CHROMIUM_PATH }
          : {}),
      });
    },
    { scope: "worker" },
  ],
});
