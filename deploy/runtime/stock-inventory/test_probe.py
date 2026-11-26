"""Offline assertion coverage. Only the hosted smoke talks to stock GARM."""
import unittest

import probe


def fixture(injections=None):
    token = "synthetic.header.signature"
    steps = [
        ("GET", "/controller", None, (409, {"error": "init_required"})),
        ("POST", "/first-run", None, (200, {"username": "stockfixture", "enabled": True, "is_admin": True})),
        ("POST", "/auth/login", None, (200, {"token": token})),
        ("PUT", "/controller", token, (200, {
            key: probe.ORIGIN for key in ("metadata_url", "callback_url", "agent_url", "webhook_url")
        })),
    ]
    for route in ("instances", "pools", "providers"):
        steps.extend([
            ("GET", "/" + route, None, (401, {})),
            ("GET", "/" + route, token, (200, [])),
        ])
    assertions = unittest.TestCase()
    observed = []

    def call(method, path, payload=None, token=None):
        index = len(observed)
        want_method, want_path, want_token, response = steps[index]
        assertions.assertEqual((method, path, token), (want_method, want_path, want_token))
        if method == "GET":
            assertions.assertIsNone(payload)
        if index == 1:
            assertions.assertGreater(len(payload["password"]), 40)
        if index == 2:
            assertions.assertEqual(payload["password"], observed[-1][2]["password"])
        if index == 3:
            assertions.assertEqual(set(payload.values()), {probe.ORIGIN, False})
        observed.append((method, path, payload))
        return (injections or {}).get(index, response)

    return call, observed, len(steps)


class ProbeTest(unittest.TestCase):
    def test_exact_workflow(self):
        call, observed, count = fixture()
        probe.probe(call)
        self.assertEqual(len(observed), count)

    def test_refuses_existing_controller_before_mutating(self):
        call, observed, _ = fixture({0: (401, {})})
        with self.assertRaises(ValueError):
            probe.probe(call)
        self.assertEqual(len(observed), 1)

    def test_refuses_noop_and_unqualified_reads(self):
        for index, response in ((1, (200, {"username": "stockfixture"})),
                                (2, (200, {"token": "not-a-bearer"})),
                                (3, (200, {})),
                                (4, (200, [])),
                                (5, (200, None)),
                                (7, (200, [{"id": "unexpected"}])),
                                (9, (302, []))):
            with self.subTest(index=index):
                call, _, _ = fixture({index: response})
                with self.assertRaises(ValueError):
                    probe.probe(call)


if __name__ == "__main__":
    unittest.main()
