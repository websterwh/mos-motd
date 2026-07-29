<template>
  <div>
    <h2 class="mb-4">MOTD Configuration</h2>
    <v-skeleton-loader v-if="loading" :loading="true" type="card" />
    <div v-else style="margin-bottom: 80px">
      <v-card class="mb-4 pa-0">
        <v-card-title class="d-flex align-center">
          <v-icon class="mr-2">mdi-monitor-dashboard</v-icon>
          <span>config.yaml</span>
        </v-card-title>
        <v-card-text>
          <span class="text-medium-emphasis text-body-2">
            Edit the plugin's <code>config.yaml</code>. See the
            <a href="https://github.com/websterwh/mos-motd#configuration" target="_blank" rel="noopener">
              README
            </a>
            for all available options (display layout, warn/crit thresholds, ignored containers, etc).
          </span>

          <v-alert v-if="error" type="error" density="compact" class="mt-4">
            {{ error }}
          </v-alert>
          <v-alert v-if="saved" type="success" density="compact" class="mt-4">
            Configuration saved.
          </v-alert>

          <v-switch
            :model-value="motdEnabled"
            @update:model-value="toggleEnabled"
            :loading="enabledSaving"
            color="primary"
            label="Show MOTD at login"
            hide-details
            density="compact"
            class="mt-4"
          />
          <div class="text-medium-emphasis text-caption mb-2">
            Turning this off only silences the automatic message at login -- running
            <code>/usr/bin/plugins/motd</code> directly and Preview MOTD below still work.
          </div>

          <v-textarea
            v-model="configYaml"
            label="config.yaml"
            rows="20"
            spellcheck="false"
            class="font-mono mt-4"
            :disabled="saving"
            hide-details="auto"
          />
        </v-card-text>
        <v-divider />
        <v-card-actions>
          <v-btn color="primary" rounded :loading="saving" @click="save">
            <v-icon start>mdi-content-save</v-icon>
            Save
          </v-btn>
          <v-btn variant="text" :loading="loading" @click="fetchSettings">
            <v-icon start>mdi-refresh</v-icon>
            Reload
          </v-btn>
          <v-btn
            variant="text"
            color="error"
            :loading="resetting"
            @click="handleResetClick"
          >
            <v-icon start>{{ resetStage === 'idle' ? 'mdi-restore' : 'mdi-alert' }}</v-icon>
            {{ resetButtonLabel }}
          </v-btn>
          <v-spacer />
          <v-btn variant="text" :loading="previewLoading" @click="preview">
            <v-icon start>mdi-eye</v-icon>
            Preview MOTD
          </v-btn>
        </v-card-actions>
      </v-card>

      <v-card v-if="previewOutput" class="mb-4 pa-0">
        <v-card-title class="d-flex align-center">
          <v-icon class="mr-2">mdi-console</v-icon>
          <span>Preview</span>
        </v-card-title>
        <v-card-text>
          <pre class="motd-preview">{{ previewOutput }}</pre>
        </v-card-text>
      </v-card>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue';

const PLUGIN_NAME = 'motd';
// How long the button stays armed before auto-reverting if the second click
// never comes, so it can't get stuck indefinitely one click away from
// wiping the config. No countdown/delay before the second click is allowed
// -- the first click arms it immediately, the second click (any time after)
// performs the reset right away.
const RESET_ARMED_TIMEOUT_MS = 5000;

const configYaml = ref('');
const loading = ref(true);
const saving = ref(false);
const resetting = ref(false);
const previewLoading = ref(false);
const previewOutput = ref('');
const error = ref('');
const saved = ref(false);

// Inline reset-button state machine instead of a popup/dialog: 'idle' ->
// (click) -> 'armed' (button label changes, an immediate second click
// resets) -> back to 'idle' either after the reset runs or after
// RESET_ARMED_TIMEOUT_MS of being armed but unused.
const resetStage = ref('idle');
let resetRevertTimer = null;

const resetButtonLabel = computed(() => {
  if (resetStage.value === 'armed') return 'Click again to reset';

  return 'Reset to Default';
});

const enabledSaving = ref(false);
const enabledLineRe = /^(\s*)enabled:\s*(true|false)\s*$/m;

// Reads the current on/off state straight out of the textarea content --
// this page doesn't otherwise parse config.yaml structurally, so rather than
// add a second source of truth, the switch just reflects and edits the
// `enabled` line within configYaml.value directly. Defaults to true (the
// binary's own default) if the key isn't present in the file yet.
const motdEnabled = computed(() => {
  const m = configYaml.value.match(enabledLineRe);

  return m ? m[2] === 'true' : true;
});

const getAuthHeaders = () => ({
  Authorization: 'Bearer ' + localStorage.getItem('authToken'),
});

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// executefunction appears to dispatch the shell function and return before
// it necessarily finishes (mos-backup's own UI polls after calling it rather
// than trusting an immediate re-fetch) -- so surface any error the response
// does carry, then give the function a moment to actually finish writing
// files before the caller trusts freshly re-fetched state.
async function callFunction(functionName) {
  const res = await fetch('/api/v1/mos/plugins/executefunction', {
    method: 'POST',
    headers: {
      ...getAuthHeaders(),
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ plugin: PLUGIN_NAME, function: functionName }),
  });

  let body = null;
  try {
    body = await res.json();
  } catch (e) {
    // Non-JSON or empty response body -- fall through to the status check.
  }

  if (!res.ok || (body && body.success === false)) {
    const msg = (body && (body.error || body.output)) || `HTTP ${res.status}`;
    throw new Error(`${functionName} failed: ${msg}`);
  }

  await sleep(800);
}

async function fetchSettings() {
  loading.value = true;
  error.value = '';
  saved.value = false;
  try {
    const res = await fetch(`/api/v1/mos/plugins/settings/${PLUGIN_NAME}`, {
      headers: getAuthHeaders(),
      cache: 'no-store',
    });
    if (!res.ok) throw new Error(`Failed to load settings (${res.status})`);
    const data = await res.json();
    configYaml.value = data.config || '';
  } catch (e) {
    error.value = e.message;
  } finally {
    loading.value = false;
  }
}

// Writes the current textarea content to MOS's settings.json store, then
// applies it to the real config.yaml the motd binary reads. Shared by Save,
// Preview (see below), and nothing else -- Reset intentionally does NOT call
// this, since it needs to discard configYaml.value rather than persist it.
async function persistConfig() {
  const res = await fetch(`/api/v1/mos/plugins/settings/${PLUGIN_NAME}`, {
    method: 'POST',
    headers: {
      ...getAuthHeaders(),
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ config: configYaml.value }),
  });
  if (!res.ok) throw new Error(`Failed to save settings (${res.status})`);

  // Saving only writes MOS's settings.json store -- the motd binary reads a
  // separate config.yaml file, so apply_config (in `functions`) copies the
  // saved value over so it actually takes effect on next login.
  await callFunction('apply_config');
}

// Runs the motd binary via the plugin query API and updates the preview
// panel. Does not touch loading/error state itself -- callers manage that,
// since both Save and the Preview button wrap this differently.
async function queryPreview() {
  const res = await fetch('/api/v1/mos/plugins/query', {
    method: 'POST',
    headers: {
      ...getAuthHeaders(),
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      command: 'motd',
      args: ['--hide-unavailable'],
      timeout: 10,
      parse_json: false,
    }),
  });
  if (!res.ok) throw new Error(`Failed to run preview (${res.status})`);
  const data = await res.json();
  if (data.success === false) throw new Error(data.error || 'motd preview failed');
  const output = data.output !== undefined ? data.output : data;
  previewOutput.value = typeof output === 'string' ? output : JSON.stringify(output, null, 2);
}

// Save persists, then also runs a preview so you see the result immediately
// without a separate click. (A prior round removed this by mistake while
// simplifying the Preview button itself -- those are two different things:
// Preview button = plain save+reload+run when you click it; Save = persist,
// then also show the result.)
async function save() {
  saving.value = true;
  error.value = '';
  saved.value = false;
  try {
    await persistConfig();
    saved.value = true;
    await queryPreview();
  } catch (e) {
    error.value = e.message;
  } finally {
    saving.value = false;
  }
}

// The switch takes effect immediately (persist + preview), rather than
// requiring a separate Save click -- that's the point of it being a switch
// rather than another textarea edit.
async function toggleEnabled(value) {
  if (enabledLineRe.test(configYaml.value)) {
    configYaml.value = configYaml.value.replace(enabledLineRe, (_match, indent) => `${indent}enabled: ${value}`);
  } else if (/^global:\s*$/m.test(configYaml.value)) {
    configYaml.value = configYaml.value.replace(/^global:\s*$/m, (match) => `${match}\n  enabled: ${value}`);
  } else {
    // No `global:` block at all (shouldn't normally happen) -- prepend one.
    configYaml.value = `global:\n  enabled: ${value}\n${configYaml.value}`;
  }

  enabledSaving.value = true;
  error.value = '';
  try {
    await persistConfig();
    await queryPreview();
  } catch (e) {
    error.value = e.message;
  } finally {
    enabledSaving.value = false;
  }
}

function clearResetTimers() {
  if (resetRevertTimer) {
    clearTimeout(resetRevertTimer);
    resetRevertTimer = null;
  }
}

// No window.confirm()/dialog here -- MOS loads this page as a remote module,
// likely inside a sandboxed iframe, where confirm()/alert() can throw
// instead of showing anything, silently aborting the whole function. The
// button itself changes label instead: a first click arms it immediately (no
// delay), and any click after that performs the reset right away.
async function handleResetClick() {
  if (resetStage.value === 'idle') {
    resetStage.value = 'armed';
    clearResetTimers();
    resetRevertTimer = setTimeout(() => {
      resetStage.value = 'idle';
    }, RESET_ARMED_TIMEOUT_MS);

    return;
  }

  // 'armed' -- this click performs the actual reset.
  clearResetTimers();
  resetStage.value = 'idle';
  resetting.value = true;
  error.value = '';
  saved.value = false;
  try {
    await callFunction('reset_config');
    await fetchSettings();
    await queryPreview();
  } catch (e) {
    error.value = e.message;
  } finally {
    resetting.value = false;
  }
}

// Preview: plain, sequential save -> reload -> run. No auto-chaining from
// Save, no relying on the just-typed configYaml.value being fresh -- persist
// it, re-fetch settings from the server so the textarea reflects exactly
// what's on disk, then run the binary against that.
async function preview() {
  previewLoading.value = true;
  error.value = '';
  try {
    await persistConfig();
    await fetchSettings();
    await queryPreview();
  } catch (e) {
    error.value = e.message;
  } finally {
    previewLoading.value = false;
  }
}

onMounted(fetchSettings);
onUnmounted(clearResetTimers);
</script>

<style scoped>
.font-mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.motd-preview {
  /* motd renders fixed-width ASCII tables/box-drawing -- wrapping (pre-wrap)
     breaks the box alignment when the panel is narrower than the table, so
     scroll horizontally instead of wrapping. */
  white-space: pre;
  overflow-x: auto;
  display: block;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.8125rem;
}
</style>
