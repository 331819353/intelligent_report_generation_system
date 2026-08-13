package compiler

// NewFieldContract stamps the contract hash a ContractStore must return with a
// field, and is the only supported way to produce one.
//
// ContractStore is an exported interface, so a store can legitimately live
// outside this package — but validateSnapshot recomputes this hash and rejects
// any mismatch. Exporting the constructor rather than the hash function keeps
// the algorithm private and makes it impossible for a store to return a hash
// computed over a different field than the one it hands back.
func NewFieldContract(field FieldContract) (FieldContract, error) {
	field.ContractHash = ""
	hash, err := fieldContractHash(field)
	if err != nil {
		return FieldContract{}, err
	}
	field.ContractHash = hash
	return field, nil
}
