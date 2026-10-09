// Synthetic `outsource config list --json` output, the same data as
// config-list.json (what the capture fake serves): the loader admits only code
// files. A drift check in tests/panel-mod.test.sh keeps the two equal.
export const CONFIG_LIST = {
  "path": "/tmp/panel-fixtures/config.json",
  "values": {
    "free.allowTraining": null,
    "free.denyPaths": null,
    "providers.agy.defaultModel": null,
    "providers.agy.enabled": null,
    "providers.muse.defaultModel": null,
    "providers.muse.enabled": false,
    "providers.openrouter.defaultModel": "nvidia/nemotron-3-ultra-550b-a55b:free",
    "providers.openrouter.enabled": null,
    "providers.xai.defaultModel": null,
    "providers.xai.enabled": null,
    "providers.zai.defaultModel": null,
    "providers.zai.enabled": null,
    "providers.zen.defaultModel": null,
    "providers.zen.enabled": null
  },
  "unknown": []
}
