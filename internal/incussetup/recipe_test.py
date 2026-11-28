import unittest
from unittest.mock import Mock
import recipe


class RecipeTests(unittest.TestCase):
    def test_preconfigured_backports_requires_exact_trusted_distribution_pair(self):
        from types import SimpleNamespace as O
        def accepted(source, codename, trusted=True):
            return recipe.distro_origin(O(origin=source, codename=codename, trusted=trusted))
        self.assertTrue(accepted("Debian Backports", "trixie-backports"))
        self.assertTrue(accepted("Debian", "trixie-security"))
        self.assertFalse(accepted("Debian Backports", "trixie-backports", False))
        self.assertFalse(accepted("third-party", "trixie-backports"))
        self.assertFalse(accepted("Debian Backports", "bookworm-backports"))
        self.assertFalse(accepted("Debian", "sid"))

    def test_fresh_install_and_verify(self):
        calls = []
        obs = Mock(side_effect=[("missing","6.0.4+deb13u1"),("pristine","6.0.4"),("configured","6.0.4")])
        r = recipe.transition(("missing","6.0.4+deb13u1"),obs,lambda v:calls.append("apt:"+v),lambda:calls.append("init"),lambda:calls.append("intent"))
        self.assertEqual(r, ("verified","configured","6.0.4"))
        self.assertEqual(calls, ["intent","apt:6.0.4+deb13u1","init"])

    def test_existing_pristine_only_initializes(self):
        init, install = Mock(), Mock()
        r = recipe.transition(("pristine","7.0.2"),Mock(side_effect=[("pristine","7.0.2"),("configured","7.0.2")]),install,init,Mock())
        self.assertEqual(r[0],"verified"); init.assert_called_once();install.assert_not_called()

    def test_preserve_and_no_replay_changed_or_partial(self):
        for expected,actual,status in [(("configured","7.0.2"),("configured","7.0.2"),"preserved"),(("pristine","7.0.2"),("configured","7.0.2"),"changed"),(("pristine","6.0.4"),("pristine","7.0.2"),"changed"),(("manual_review",""),("manual_review",""),"refused"),(("unsupported",""),("unsupported",""),"refused")]:
            mutate=Mock(side_effect=AssertionError)
            self.assertEqual(recipe.transition(expected,lambda:actual,mutate,mutate,mutate)[0],status)
            mutate.assert_not_called()

    def test_apt_or_daemon_failure_is_unknown_not_success(self):
        fail=Mock(side_effect=recipe.Unknown)
        with self.assertRaises(recipe.Unknown):
            recipe.transition(("missing","6.0.4+deb13u1"),lambda:("missing","6.0.4+deb13u1"),fail,fail,Mock())
        with self.assertRaises(recipe.Unknown):
            recipe.transition(("missing","6.0.4+deb13u1"),Mock(side_effect=[("missing","6.0.4+deb13u1"),("manual_review","")]),Mock(),fail,Mock())

    def test_configured_nondefault_pool_preserved(self):
        server={"auth":"trusted","environment":{"server":"incus","server_version":"7.0.2","server_clustered":False},"config":{}}
        root={"name":"default","devices":{"disk":{"type":"disk","pool":"pool1","path":"/"}},"config":{}}
        self.assertEqual(recipe.classify(server,[{"name":"pool1","driver":"btrfs","status":"Created"}],root,Mock(side_effect=AssertionError)),("configured","7.0.2"))
        self.assertEqual(recipe.classify(server,[{"name":"pool1","driver":"btrfs","status":"Unavailable"}],root,Mock(side_effect=AssertionError))[0],"manual_review")

    def test_existing_network_only_is_not_pristine(self):
        server={"auth":"trusted","environment":{"server":"incus","server_version":"7.0.2","server_clustered":False},"config":{}}
        root={"name":"default","devices":{},"config":{}}
        inventory={"/projects":["/1.0/projects/default"],"/profiles":["/1.0/profiles/default"],"/instances?all-projects=true":[],"/images":[],"/certificates":[],"/networks?recursion=1":[{"managed":True}]}
        self.assertEqual(recipe.classify(server,[],root,inventory.__getitem__)[0],"manual_review")
        inventory["/networks?recursion=1"]=[{"managed":False}]
        self.assertEqual(recipe.classify(server,[],root,inventory.__getitem__)[0],"pristine")


if __name__=="__main__":
    unittest.main()
