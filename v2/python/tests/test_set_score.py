"""O score não pode ser alterado por update() — falha medida em 2026-08-15.

PATCH /api/v1/records/{id} não tem campo score: o servidor responde 200 e
descarta a chave. Um cliente que chamasse update(id, score=8) — exemplo que a
própria docstring ensinava — recebia sucesso e nada era gravado. É a mesma
forma do defeito do campo `archived` em 2026-06-16.

A correção não é fazer update() funcionar por baixo dos panos: dividir a
chamada em duas criaria sucesso parcial (o summary grava, o score falha), que é
a mesma classe de perda silenciosa por outra porta. update() recusa ALTO e
aponta o método certo.
"""
import asyncio
import unittest

from anhurdb.client import Memory


class SetScoreContractTest(unittest.TestCase):
    def setUp(self):
        self.memory = Memory(api_key="test-key", url="https://example.invalid")

    def test_update_refuses_score_instead_of_dropping_it(self):
        """Antes desta guarda a chamada devolvia sucesso e não gravava nada."""
        with self.assertRaises(ValueError) as raised:
            asyncio.run(self.memory.update(42, score=8))

        message = str(raised.exception)
        self.assertIn("set_score", message,
                      "a mensagem tem que nomear o método que funciona")

    def test_update_refuses_score_even_alongside_other_fields(self):
        """O caso perigoso: summary gravaria, score não — sucesso parcial."""
        with self.assertRaises(ValueError):
            asyncio.run(self.memory.update(42, summary="novo", score=8))

    def test_update_still_accepts_ordinary_fields(self):
        """A guarda não pode desligar o update para todo mundo.

        Sem conexão aberta o cliente levanta AnhurConnectionError — o que
        importa é que NÃO é o ValueError da guarda, ou seja, a chamada passou
        da validação e chegou ao transporte.
        """
        with self.assertRaises(Exception) as raised:
            asyncio.run(self.memory.update(42, summary="novo"))
        self.assertNotIsInstance(raised.exception, ValueError)

    def test_set_score_rejects_values_outside_the_schema_range(self):
        for invalid_score in (0, -1, 11, 99):
            with self.subTest(score=invalid_score):
                with self.assertRaises(ValueError):
                    asyncio.run(self.memory.set_score(42, invalid_score))

    def test_set_score_accepts_the_boundaries(self):
        """1 e 10 são válidos: a guarda não pode cortar as pontas da faixa."""
        for valid_score in (1, 10):
            with self.subTest(score=valid_score):
                with self.assertRaises(Exception) as raised:
                    asyncio.run(self.memory.set_score(42, valid_score))
                self.assertNotIsInstance(raised.exception, ValueError)


if __name__ == "__main__":
    unittest.main()
