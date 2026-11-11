import { StatusBar } from 'expo-status-bar';
import { useEffect, useRef, useState } from 'react';
import { ActivityIndicator, Linking, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';
import { SafeAreaProvider, SafeAreaView } from 'react-native-safe-area-context';
import { File, FileMode } from 'expo-file-system';
import { fetch } from 'expo/fetch';
import Constants from 'expo-constants';
import { resolveApiUrl } from './apiUrl';
import { type NoteResult } from './noteResult';
import { uploadNote } from './uploadNote';
import {
  AudioQuality,
  IOSOutputFormat,
  RecordingPresets,
  requestRecordingPermissionsAsync,
  setAudioModeAsync,
  useAudioRecorder,
  useAudioRecorderState,
  type RecordingOptions,
} from 'expo-audio';

// Where the Go backend lives. By default the app derives it from the Expo dev server's
// host (same computer, port 8080), so it follows your IP with nothing to configure.
// EXPO_PUBLIC_API_URL overrides it, and the field in the app can be edited at any time.
const DEFAULT_API_URL = resolveApiUrl({
  envUrl: process.env.EXPO_PUBLIC_API_URL,
  hostUri: Constants.expoConfig?.hostUri,
});
const MAX_RECORDING_MS = 4 * 60 * 1000; // 16 kHz mono PCM is ~1.9 MB/min; the backend caps files at 10 MiB
const UPLOAD_TIMEOUT_MS = 90_000; // a little over the backend's own OpenAI + Notion timeouts

// The backend accepts WAV and MP3 only. expo-audio's default preset records
// AAC (.m4a), so on iOS we ask for 16-bit linear PCM in a .wav file instead.
// Android has no WAV option in expo-audio; it falls back to .m4a, which the
// format check below reports before anything is uploaded.
const RECORDING_OPTIONS: RecordingOptions = {
  ...RecordingPresets.HIGH_QUALITY,
  extension: '.wav',
  sampleRate: 16000,
  numberOfChannels: 1,
  bitRate: 256000,
  ios: {
    ...RecordingPresets.HIGH_QUALITY.ios,
    extension: '.wav',
    sampleRate: 16000,
    outputFormat: IOSOutputFormat.LINEARPCM,
    audioQuality: AudioQuality.MAX,
    linearPCMBitDepth: 16,
    linearPCMIsBigEndian: false,
    linearPCMIsFloat: false,
  },
};

type Recording = { uri: string; format: 'wav' | 'mp3' | null; label: string; sizeBytes: number };

// Read just the file's first 12 bytes to see what was really recorded,
// mirroring the backend's check (and without loading the whole file).
async function inspectRecording(uri: string): Promise<Recording> {
  const handle = new File(uri).open(FileMode.ReadOnly);
  let head: Uint8Array;
  let sizeBytes: number;
  try {
    head = await handle.readBytes(12);
    sizeBytes = handle.size ?? 0;
  } finally {
    handle.close();
  }
  const ascii = (from: number, to: number) => String.fromCharCode(...Array.from(head.slice(from, to)));

  let format: Recording['format'] = null;
  let label = 'unknown format';
  if (ascii(0, 4) === 'RIFF' && ascii(8, 12) === 'WAVE') [format, label] = ['wav', 'WAV'];
  else if (ascii(0, 3) === 'ID3') [format, label] = ['mp3', 'MP3'];
  else if (ascii(4, 8) === 'ftyp') label = 'M4A/AAC';
  else if (ascii(0, 4) === 'caff') label = 'CAF';

  return { uri, format, label, sizeBytes };
}

const formatSize = (bytes: number) => (bytes >= 1e6 ? `${(bytes / 1e6).toFixed(1)} MB` : `${Math.round(bytes / 1e3)} KB`);
const formatTime = (ms: number) => {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
};

export default function App() {
  return (
    <SafeAreaProvider>
      <Screen />
    </SafeAreaProvider>
  );
}

function Screen() {
  const recorder = useAudioRecorder(RECORDING_OPTIONS);
  const recorderState = useAudioRecorderState(recorder);

  const [apiUrl, setApiUrl] = useState(DEFAULT_API_URL);
  const [recording, setRecording] = useState<Recording | null>(null);
  const [uploading, setUploading] = useState(false);
  const [result, setResult] = useState<NoteResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const stopping = useRef(false);
  const isRecording = recorderState.isRecording;
  const canUpload = !!recording?.format && !isRecording && !uploading;

  async function startRecording() {
    setError(null);
    setResult(null);
    setRecording(null);
    try {
      const { granted } = await requestRecordingPermissionsAsync();
      if (!granted) {
        setError('Microphone permission was denied. Enable it in Settings and try again.');
        return;
      }
      await setAudioModeAsync({ allowsRecording: true, playsInSilentMode: true });
      await recorder.prepareToRecordAsync();
      recorder.record();
    } catch (e) {
      setError(`Could not start recording: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  async function stopRecording() {
    if (stopping.current) return; // the auto-stop below can fire again while stop() is still finishing
    stopping.current = true;
    try {
      await recorder.stop();
      if (!recorder.uri) throw new Error('no recording file was produced');
      setRecording(await inspectRecording(recorder.uri));
    } catch (e) {
      setError(`Could not stop recording: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      stopping.current = false;
    }
  }

  // Stop automatically so a long note can't exceed the backend's upload limit.
  useEffect(() => {
    if (isRecording && recorderState.durationMillis >= MAX_RECORDING_MS) void stopRecording();
  }, [isRecording, recorderState.durationMillis]);

  async function upload() {
    if (!recording?.format) return;
    setUploading(true);
    setError(null);
    setResult(null);
    try {
      const form = new FormData();
      // expo-file-system's File is a Blob, so it can be appended directly. (The old
      // React Native { uri, name, type } object is not supported by expo/fetch.)
      form.append('audio', new File(recording.uri));
      setResult(await uploadNote({ baseUrl: apiUrl, body: form, fetchFn: fetch, timeoutMs: UPLOAD_TIMEOUT_MS }));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setUploading(false);
    }
  }

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar style="dark" />
      <ScrollView contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
        <Text style={styles.title}>Voice Notes</Text>

        <Text style={styles.label}>Backend URL</Text>
        <TextInput
          style={styles.input}
          value={apiUrl}
          onChangeText={setApiUrl}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
          placeholder="http://192.168.1.20:8080"
        />

        <Text style={styles.timer}>{formatTime(recorderState.durationMillis)}</Text>
        <Text style={styles.status}>
          {isRecording
            ? 'Recording…'
            : recording
              ? `Recorded ${recording.label} · ${formatSize(recording.sizeBytes)}`
              : 'Ready to record'}
        </Text>
        {recording && !recording.format && (
          <Text style={styles.warning}>
            This device recorded {recording.label}, but the backend only accepts WAV or MP3. Use an iPhone for the demo.
          </Text>
        )}

        <View style={styles.row}>
          <Button title="Record" color="#d92d20" onPress={startRecording} disabled={isRecording || uploading} />
          <Button title="Stop" color="#1d2939" onPress={stopRecording} disabled={!isRecording} />
          <Button title="Upload" color="#175cd3" onPress={upload} disabled={!canUpload} />
        </View>

        {uploading && (
          <View style={styles.progress}>
            <ActivityIndicator />
            <Text style={styles.status}>Transcribing and saving to Notion…</Text>
          </View>
        )}

        {error && <Text style={styles.error}>{error}</Text>}

        {result && (
          <View style={styles.card}>
            <Text style={styles.cardHeading}>Summary</Text>
            <Text style={styles.body}>{result.summary}</Text>

            <Text style={styles.cardHeading}>Action items</Text>
            {result.action_items.length === 0 ? (
              <Text style={styles.body}>None mentioned in the recording.</Text>
            ) : (
              result.action_items.map((item, i) => (
                <Text key={i} style={styles.body}>
                  • {item}
                </Text>
              ))
            )}

            {result.tags.length > 0 && <Text style={styles.tags}>{result.tags.map((t) => `#${t}`).join('  ')}</Text>}

            <Pressable style={styles.link} onPress={() => Linking.openURL(result.notion_page_url).catch(() => setError('Could not open the Notion page.'))}>
              <Text style={styles.linkText}>Open in Notion ↗</Text>
            </Pressable>
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
}

function Button(props: { title: string; color: string; onPress: () => void; disabled?: boolean }) {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled: !!props.disabled }}
      disabled={props.disabled}
      onPress={props.onPress}
      style={[styles.button, { backgroundColor: props.color }, props.disabled && styles.buttonDisabled]}
    >
      <Text style={styles.buttonText}>{props.title}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  safe: { flex: 1, backgroundColor: '#f9fafb' },
  container: { padding: 20, gap: 8 },
  title: { fontSize: 28, fontWeight: '700', color: '#101828', marginBottom: 8 },
  label: { fontSize: 13, color: '#475467' },
  input: {
    borderWidth: 1,
    borderColor: '#d0d5dd',
    borderRadius: 8,
    padding: 10,
    fontSize: 15,
    backgroundColor: '#fff',
    color: '#101828',
  },
  timer: { fontSize: 48, fontWeight: '300', textAlign: 'center', color: '#101828', marginTop: 16 },
  status: { fontSize: 15, textAlign: 'center', color: '#475467' },
  warning: { fontSize: 14, textAlign: 'center', color: '#b54708' },
  row: { flexDirection: 'row', gap: 10, marginVertical: 16 },
  button: { flex: 1, paddingVertical: 14, borderRadius: 10, alignItems: 'center' },
  buttonDisabled: { opacity: 0.35 },
  buttonText: { color: '#fff', fontSize: 16, fontWeight: '600' },
  progress: { flexDirection: 'row', gap: 10, justifyContent: 'center', alignItems: 'center' },
  error: { color: '#b42318', backgroundColor: '#fef3f2', padding: 12, borderRadius: 8, fontSize: 14 },
  card: { backgroundColor: '#fff', borderRadius: 12, padding: 16, gap: 6, borderWidth: 1, borderColor: '#eaecf0' },
  cardHeading: { fontSize: 13, fontWeight: '700', color: '#475467', textTransform: 'uppercase', marginTop: 8 },
  body: { fontSize: 16, color: '#101828', lineHeight: 22 },
  tags: { fontSize: 14, color: '#175cd3', marginTop: 8 },
  link: { marginTop: 14, paddingVertical: 12, borderRadius: 10, backgroundColor: '#101828', alignItems: 'center' },
  linkText: { color: '#fff', fontSize: 16, fontWeight: '600' },
});
