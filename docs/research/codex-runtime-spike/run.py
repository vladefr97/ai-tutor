"""Throwaway local app-server smoke test. Uses the owner's ChatGPT subscription.
Only a temporary copy of auth.json enters each isolated container; no host history.
"""
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import tempfile
import threading
import time
import uuid

IMAGE = 'tutor-codex-spike:0.155.1'
OUT = Path(__file__).resolve().parent

class Client:
    def __init__(self, root, label, auth):
        self.name = 'tutor-codex-spike-' + uuid.uuid4().hex[:12]
        self.cwd = root / label
        self.cwd.mkdir()
        home = root / (label + '-auth')
        home.mkdir(mode=0o700)
        shutil.copyfile(auth, home / 'auth.json')
        (home / 'auth.json').chmod(0o600)
        self.proc = subprocess.Popen([
            'docker', 'run', '--rm', '-i', '--name', self.name,
            '--mount', f'type=bind,source={home},target=/codex',
            '--mount', f'type=bind,source={self.cwd},target=/workspace',
            '-e', 'CODEX_HOME=/codex', IMAGE,
        ], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True)
        self.q = queue.Queue()
        self.seq = 0
        self.pending = []
        threading.Thread(target=self.reader, daemon=True).start()

    def reader(self):
        for line in self.proc.stdout:
            try:
                event = json.loads(line)
                self.q.put(event)
            except json.JSONDecodeError:
                pass
        self.q.put({'closed': True})

    def send(self, obj):
        self.proc.stdin.write(json.dumps(obj) + '\n')
        self.proc.stdin.flush()

    def recv(self, deadline):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError('Timed out waiting for app-server')
        try:
            event = self.q.get(timeout=remaining)
        except queue.Empty:
            raise RuntimeError('Timed out waiting for app-server') from None
        if event.get('closed'):
            raise RuntimeError('app-server exited unexpectedly')
        return event

    def request(self, method, params):
        self.seq += 1
        self.send({'id': self.seq, 'method': method, 'params': params})
        deadline = time.monotonic() + 90
        while True:
            event = self.recv(deadline)
            if event.get('id') == self.seq and 'method' not in event:
                if 'error' in event:
                    raise RuntimeError(method + ': ' + str(event['error'].get('message', 'RPC error')))
                return event['result']
            self.pending.append(event)

    def start(self):
        self.request('initialize', {'clientInfo': {'name': 'tutor_runtime_spike', 'version': '0.1.0'}})
        self.send({'method': 'initialized'})
        account = self.request('account/read', {'refreshToken': False}).get('account') or {}
        if account.get('type') != 'chatgpt':
            raise RuntimeError('Expected ChatGPT authentication, got ' + str(account.get('type')))
        result = self.request('thread/start', {
            'cwd': '/workspace', 'approvalPolicy': 'never',
            'sandbox': 'danger-full-access',
        })
        self.thread = result['thread']['id']
        self.model = result.get('model')
        return self.thread

    def turn(self, prompt):
        self.pending.clear()
        result = self.request('turn/start', {'threadId': self.thread,
            'input': [{'type': 'text', 'text': prompt}]})
        turn_id = result['turn']['id']
        answers = []
        deadline = time.monotonic() + 60
        while True:
            event = self.pending.pop(0) if self.pending else self.recv(deadline)
            method = event.get('method')
            params = event.get('params', {})
            if method and 'id' in event:
                self.send({'id': event['id'], 'error': {'code': -32601, 'message': 'Interactive requests unsupported in this smoke test'}})
            if method == 'item/completed' and params.get('turnId') == turn_id:
                item = params.get('item', {})
                if item.get('type') == 'agentMessage':
                    answers.append(item['text'])
            if method == 'turn/completed' and params.get('turn', {}).get('id') == turn_id:
                turn = params['turn']
                if turn.get('status') != 'completed':
                    raise RuntimeError('Model turn did not complete: ' + json.dumps(turn.get('error')))
                return '\n'.join(answers)

    def close(self):
        subprocess.run(['docker', 'stop', '-t', '1', self.name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            self.proc.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.communicate()


def main():
    auth = Path.home() / '.codex' / 'auth.json'
    if not auth.is_file():
        raise RuntimeError('Run codex login first')
    clients = []
    with tempfile.TemporaryDirectory(prefix='tutor-codex-spike-') as tmp:
        root = Path(tmp)
        try:
            planning = Client(root, 'planning', auth)
            clients.append(planning)
            first_id = planning.start()
            print('Planning session started; model=' + str(planning.model), flush=True)
            marker = 'private-interview-' + uuid.uuid4().hex
            question = planning.turn('This is a synthetic tutor test. No tools needed. Private interview marker: ' + marker + '. Do not include that marker in any plan. Ask exactly one short question about my Python experience. No other text.')
            assert question.strip(), 'Interview question missing'
            plan_text = planning.turn('I know variables but not functions. My goal is writing a function that adds two integers. Prepare a tiny course plan with exactly one lesson and one exercise. Return ONLY JSON with keys goal, lesson, exercise; values must be strings. No markdown, no tools, no private interview marker.')
            plan = json.loads(plan_text.removeprefix('```json').removesuffix('```').strip())
            assert set(plan) == {'goal', 'lesson', 'exercise'}
            assert all(isinstance(v, str) and v for v in plan.values())
            assert marker not in json.dumps(plan)
            print('Synthetic interview and plan generated.', flush=True)
            generation = Client(root, 'generation', auth)
            clients.append(generation)
            (generation.cwd / 'approved-plan.json').write_text(json.dumps(plan, ensure_ascii=False, indent=2))
            second_id = generation.start()
            assert first_id != second_id
            assert list(generation.cwd.iterdir()) == [generation.cwd / 'approved-plan.json']
            generation.turn('This is an isolated course-generation smoke test. The synthetic learner has approved approved-plan.json. Read it and create lesson.md with a short explanation, one worked Python example, and one exercise matching the plan. Do not read files outside /workspace. Do not install anything. Keep the file under 250 words. Finish with a brief confirmation.')
            lesson = (generation.cwd / 'lesson.md').read_text()
            assert len(lesson.strip()) > 80, 'Missing substantive lesson artifact'
            assert marker not in lesson, 'Private marker leaked into course'
            (OUT / 'sample-lesson.md').write_text(lesson)
            report = {'runtime': IMAGE, 'auth': 'chatgpt',
                'planning_thread': first_id, 'generation_thread': second_id,
                'planning_model': planning.model, 'generation_model': generation.model,
                'checks': {'real_interview': True, 'real_plan': True, 'distinct_threads': True,
                    'separate_containers_workspaces_and_homes': True, 'only_approved_plan_transferred': True,
                    'lesson_file_created': True, 'private_marker_not_in_artifact': True},
                'limitations': ['Default models used; stronger/cheaper model routing not evaluated.',
                    'Synthetic prompts; actual tutoring skills and grading quality not evaluated.',
                    'No Go adapter, GitHub integration or product UI tested.',
                    'Marker check is a smoke test, not proof of complete information isolation.']}
            (OUT / 'result.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
            print(json.dumps(report, ensure_ascii=False, indent=2), flush=True)
        finally:
            for client in clients:
                client.close()

if __name__ == '__main__':
    main()
