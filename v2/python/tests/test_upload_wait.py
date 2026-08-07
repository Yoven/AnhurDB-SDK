"""wait_for_upload — polling tolerante a 404 transiente (paridade 3 SDKs).

Junior Tip [por que 404 vira "pendente" no começo — medido 2026-08-07]: as
leituras do AnhurDB são load-balanced; logo após o POST de upload um follower
que ainda não aplicou a entrada devolve 404 legítimo por alguns segundos
(read-your-writes). Antes deste helper o runner Go tolerava, o Python morria e
o TS passava por sorte de timing. Dentro de not_found_grace o 404 é espera;
depois dela é erro real — id inválido não pode virar espera infinita.

Estes testes stubam ``upload_status`` (a unidade sob teste é a máquina de
estados do wait, não o transporte); o carimbo de ``status_code`` nas exceções é
testado direto na classe.
"""

import unittest

from anhurdb.client import Memory
from anhurdb.client.exceptions import (
    AnhurError,
    AnhurQueryError,
    AnhurUploadWaitTimeout,
)


def _not_found() -> AnhurQueryError:
    return AnhurQueryError("Resource not found (HTTP 404): /api/v1/upload/9/status", status_code=404)


class TestExceptionStatusCode(unittest.TestCase):
    def test_status_code_is_carried(self) -> None:
        error = AnhurQueryError("boom", status_code=404)
        self.assertEqual(error.status_code, 404)

    def test_status_code_defaults_to_none(self) -> None:
        self.assertIsNone(AnhurQueryError("boom").status_code)

    def test_plain_message_still_works(self) -> None:
        self.assertIn("boom", str(AnhurQueryError("boom", status_code=500)))


class TestWaitForUpload(unittest.IsolatedAsyncioTestCase):
    def _memory_with_stub(self, responses: list) -> Memory:
        """Memory cujo upload_status devolve/levanta cada item da lista em ordem;
        o último item repete para sempre."""
        memory = Memory(api_key="test-key-000000000000000000000000")
        state = {"index": 0}

        async def stubbed_upload_status(upload_id: int):
            current = responses[min(state["index"], len(responses) - 1)]
            state["index"] += 1
            if isinstance(current, Exception):
                raise current
            return current

        memory.upload_status = stubbed_upload_status  # type: ignore[method-assign]
        return memory

    async def test_tolerates_early_404_then_completes(self) -> None:
        memory = self._memory_with_stub([
            _not_found(),
            _not_found(),
            {"record_id": 42, "status": "completed", "completed": True},
        ])
        result = await memory.wait_for_upload(
            42, timeout=5.0, interval=0.01, not_found_grace=2.0
        )
        self.assertTrue(result["completed"])

    async def test_404_beyond_grace_raises_the_real_error(self) -> None:
        memory = self._memory_with_stub([_not_found()])
        with self.assertRaises(AnhurQueryError) as caught:
            await memory.wait_for_upload(
                999999, timeout=5.0, interval=0.01, not_found_grace=0.05
            )
        self.assertEqual(caught.exception.status_code, 404)

    async def test_failed_status_is_terminal_data_not_error(self) -> None:
        memory = self._memory_with_stub([
            {"record_id": 7, "status": "failed", "error": "extract crashed"},
        ])
        result = await memory.wait_for_upload(7, timeout=2.0, interval=0.01)
        self.assertEqual(result["status"], "failed")

    async def test_timeout_raises_typed_error_with_last_status(self) -> None:
        memory = self._memory_with_stub([{"record_id": 8, "status": "processing"}])
        with self.assertRaises(AnhurUploadWaitTimeout) as caught:
            await memory.wait_for_upload(8, timeout=0.1, interval=0.01)
        self.assertIn("processing", str(caught.exception))

    async def test_timeout_error_is_an_anhur_error(self) -> None:
        self.assertTrue(issubclass(AnhurUploadWaitTimeout, AnhurError))


if __name__ == "__main__":
    unittest.main()
