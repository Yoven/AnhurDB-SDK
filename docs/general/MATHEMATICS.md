# Unified Mathematics of AnhurDB

This document defines the canonical formulas implemented in AnhurSDK (Go/Python).

## 1. Cognitive Weight ($w$)
The gravity or importance of a memory.
$$w = \alpha R + \beta V + \gamma C$$
- $\alpha$: Recency weight.
- $\beta$: Absolute value (LLM Score).
- $\gamma$: Correlation weight (Graph degree).

## 2. Ebbinghaus Recency ($R$)
How fast a memory is forgotten (moves from episodic to decayed).
$$R = \exp(-\lambda \times t)$$
- $\lambda$: Decay constant (default 0.01).
- $t$: Time elapsed in days.

## 3. Dimensional Spectrum ($D$)
Determines the storage fidelity (bits) based on importance ($w$).
$$D = D_{min} + \frac{D_{max} - D_{min}}{1 + \exp(-\kappa(w - \mu))}$$
- $\kappa$: Sigmoid steepness.
- $\mu$: Inflection point (threshold for "high importance").
- **Snap:** Results are snapped to the nearest valid dimension (e.g., 64, 128, 1024...).

## 4. Hamming Similarity ($S$)
Distance in the semantic bit-space.
$$S = 1 - \frac{H(a, b)}{D}$$
- $H$: Hamming Distance (XOR + Popcount).
- $D$: Active dimension of the target record.

---

> **Note:** These formulas are interpreted by the `formulas.py` and `math.go` files, using constants injected from `core.yaml`.
