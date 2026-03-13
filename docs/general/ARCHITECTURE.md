# Architecture: The Bridge Pattern

## 1. Context
Managing a hybrid ecosystem (Go Core + Python Agents) creates a high risk of **Architectural Drift**. If the Go server uses `Alpha=0.3` for importance and the Python Agent uses `Alpha=0.5`, the "Brain" becomes schizophrenic—memories are saved with one criteria and processed with another.

## 2. The Solution: Single Source of Truth (SSOT)
AnhurSDK implements the **Bridge Pattern** by externalizing the contract:

### A. The Schema (`core.yaml`)
Owned by the **AnhurDB** (Core) but linked into the **AnhurSDK**. 
It defines:
- **Taxonomy:** Valid memory types (`episodic`, `fact`, `emotion`, etc.).
- **Constants:** Decay values, sigmoid slopes, weighting factors.
- **Protocols:** How a `MemoryRecord` looks in JSON.

### B. Automated Codegen
We avoid manual implementation of models. `codegen.py` transforms the YAML into:
- `golang/generated_schema.go`
- `python/anhurdb/generated_schema.py`

### C. Consumption Model
- **AnhurDB (Core):** Imports the Go generated constants for its native Go Regression Worker.
- **AnhurAgents (Cortical):** Imports the Python generated constants for its LLM processing.
- **Result:** Exact mathematical parity.
    
### D. The Mathematical Heart (`formulas.py`)
All mathematical logic for weight calculation and adaptive dimension is centrally implemented in [formulas.py](../../python/anhurdb/formulas.py). 
- Agents **MUST** use this canonical logic rather than re-implementing it.
- This ensures that a memory's importance ($w$) and fidelity ($D$) are evaluated identically across all specialized agents.

## 3. Deployment Flow
1. **Developer** modifies `core.yaml`.
2. **Codegen** updates drivers.
3. **CI/CD** runs tests against both drivers.
4. **Release** publishes a new version of the SDK.
5. **Projects** update their dependencies to the new SDK version.

---

## 🔗 Repository Links (Local Map)
- **AnhurDB:** `../AnhurDB` (Owns the YAML)
- **AnhurAgents:** `../AnhurAgents` (Consumes the SDK)
- **AnhurSDK:** `.` (Governs the Contract)
