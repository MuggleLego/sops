package blakley

import (
	"crypto/rand"
	"fmt"
)

const (
	// ShareOverhead is the byte size overhead of each share
	// when using Split on a secret. This is caused by appending
	// a one byte tag to the share.
	ShareOverhead = 1
)

// makeCoordinate generate a point on a given degree hyperplane.
// If flag == true,
// set the given intercept to the first component.
func makeCoordinate(flag bool, intercept uint8, degree int) ([]byte, error) {
	//Create a new coordinate
	p := make([]byte, degree)

	//Assign random components to the point.
	if _, err := rand.Read(p); err != nil {
		return p, err
	}

	//Set the intercept to the first component
	if flag {
		p[0] = intercept
	}

	return p, nil
}

// mult multiplies two numbers in GF(2^8)
// GF(2^8) multiplication using log/exp tables
func mult(a, b uint8) (out uint8) {
	if a == 0 || b == 0 {
		return 0
	}
	log_a := logTable[a]
	log_b := logTable[b]
	sum := (int(log_a) + int(log_b)) % 255
	return expTable[sum]
}

// div divides two numbers in GF(2^8)
// GF(2^8) division using log/exp tables
func div(a, b uint8) uint8 {
	if b == 0 {
		// leaks some timing information but we don't care anyways as this
		// should never happen, hence the panic
		panic("divide by zero")
	}

	return mult(a, invTabel[b])
}

// Intersect:{x_0,x_1,...,x_{t-1}}
// share[i]:{a_0,a_1,...,a_t-2,dummy}
// Evaluate c_i = x_{t-1} - /Sigma_{j=0}^{t-2} a_j*x_j
func evaluate(share, intersect []byte, threshold int) uint8 {
	if len(intersect) != threshold || len(share) != threshold {
		panic("initialize failed")
	}
	//ret = x_{t-1}
	ret := intersect[threshold-1]

	//ret = x_{t-1} - /Sigma_{j=0}^{t-2} a_j*x_j
	for i := 0; i < threshold-1; i++ {
		ret ^= mult(share[i], intersect[i])
	}
	return ret
}

// Solve uses the Gaussian-elimination algorithm to solve a linear system
// Ax = b and returns the first compponent of the solution,
// which is the reconstruction of the secret bytes.
func solve(matrix [][]byte, b []byte, threshold int) (uint8, error) {
	//Invalid input of a null vector
	if b == nil {
		return 0, fmt.Errorf("reconstruct phase failed")
	}
	//Invalid input of a null matrix
	if matrix == nil || len(matrix) != threshold {
		return 0, fmt.Errorf("reconstruct phase failed")
	} else {
		for i := range matrix {
			if matrix[i] == nil {
				return 0, fmt.Errorf("reconstruct phase failed")
			}
			//Can't match the number of rows and columns of the matrix
			if len(matrix[i]) != threshold {
				return 0, fmt.Errorf("reconstruct phase failed")
			}
		}
	}

	ret := make([]byte, threshold)

	//find the pivot of column i
	for i := 0; i < threshold; i++ {
		//Find the pivot of column i
		if matrix[i][i] == 0 {
			for j := i + 1; j < threshold; j++ {
				if matrix[j][i] != 0 {
					//Swap the columns
					matrix[i], matrix[j] = matrix[j], matrix[i]
					b[i], b[j] = b[j], b[i]
					break
				}
			}
		}
		if matrix[i][i] == 0 {
			return 0, fmt.Errorf("matrix is singular")
		}

		//Elimination for all rows below the pivot
		for j := i + 1; j < threshold; j++ {
			factor := div(matrix[j][i], matrix[i][i])
			//Do for all remaining elements in current row j
			for k := i; k < threshold; k++ {
				matrix[j][k] ^= mult(matrix[i][k], factor)
			}
			b[j] ^= mult(b[i], factor)
		}
	}

	//Back substitute to get the solution
	for i := threshold - 1; i >= 0; i-- {
		ret[i] = b[i]
		for j := i + 1; j < threshold; j++ {
			ret[i] ^= mult(ret[j], matrix[i][j])
		}
		ret[i] = div(ret[i], matrix[i][i])
	}
	return ret[0], nil
}

func compress(shares [][][]byte) [][]byte {
	parts := len(shares)
	blen := len(shares[0])
	threshold := len(shares[0][0])
	ret := make([][]byte, parts)
	for i, v := range shares {
		ret[i] = make([]byte, 0, blen*threshold+1)
		for _, s := range v {
			ret[i] = append(ret[i], s...)
		}
		ret[i] = append(ret[i], byte(threshold))
	}
	return ret
}

func decompress(shares [][]byte) ([][][]byte, error) {
	parts := len(shares)
	tmp := len(shares[0]) - 1
	threshold := int(shares[0][tmp])
	if threshold < 2 || parts < threshold {
		return nil, fmt.Errorf("not enough shares to reconstruct")
	}
	var blen int
	if tmp%threshold != 0 {
		return nil, fmt.Errorf("decompress failed")
	} else {
		blen = tmp / threshold
	}
	ret := make([][][]byte, parts)
	for i := range ret {
		ret[i] = make([][]byte, blen)
		for j := range ret[i] {
			ret[i][j] = make([]byte, threshold)
			copy(ret[i][j], shares[i][j*threshold:(j+1)*threshold])
		}
	}
	return ret, nil
}

// Split takes an arbitrarily long secret and generates a `parts`
// number of shares, `threshold` of which are required to reconstruct
// the secret. The parts and threshold must be at least 2, and less
// than 256. The returned shares are subsecret of each party of each
// secret byte 'secret[i]'.
func Split(secret []byte, parts, threshold int) ([][]byte, error) {
	//Sanity check for the secret
	if parts < threshold {
		return nil, fmt.Errorf("parts cannot be less than threshold")
	}
	if parts > 255 {
		return nil, fmt.Errorf("parts cannot exceed 255")
	}
	if threshold < 2 {
		return nil, fmt.Errorf("threshold must be at least 2")
	}
	if threshold > 255 {
		return nil, fmt.Errorf("threshold cannot exceed 255")
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("cannot split an empty secret")
	}
	shares := make([][][]byte, parts)
	intersect := make([][]byte, len(secret))
	var err error
	//The generate part.
	for idx := range secret {
		//Generate for each secret byte secret[idx].
		//More precisely, generate (x_0,x_1,x_2,...,x_t-1)
		//Where x_0 is the secret[idx].
		intersect[idx], err = makeCoordinate(true, secret[idx], threshold)
		if err != nil {
			return nil, fmt.Errorf("failed to generate secret intersection: %w", err)
		}
	}

	for idx := range shares {
		//Each party holds a share of the every secret byte.
		shares[idx] = make([][]byte, len(secret))
		for i := range shares[idx] {
			//Initialize for each secret byte, generate (a_0,a_1,...,a_t-2)
			shares[idx][i], err = makeCoordinate(false, 0, threshold)
			if err != nil {
				return nil, fmt.Errorf("failed to generate secret share: %w", err)
			}
		}
	}

	//For the ith party
	for i := range shares {
		//For the jth secret byte
		for j := range shares[i] {
			shares[i][j][threshold-1] = evaluate(shares[i][j], intersect[j], threshold)
		}
	}

	return compress(shares), nil
}

func Combine(share [][]byte) ([]byte, error) {
	//Sanity check for the shares
	if share == nil {
		return nil, fmt.Errorf("cannot combine nil shares")
	}
	for i := range share {
		if share[i] == nil {
			return nil, fmt.Errorf("the %vth share invalid,cannot combine nil shares", i)
		}
		if len(share[i]) != len(share[0]) {
			return nil, fmt.Errorf("invalid shares provided:party %v", i)
		}
	}
	shares, err := decompress(share)
	if err != nil {
		return nil, fmt.Errorf("failed to reconstruct: %w", err)
	}
	b := len(shares[0])
	t := len(shares[0][0])

	if len(shares) < t || len(shares) < 2 {
		return nil, fmt.Errorf("not enough shares provided")
	}
	secret := make([]byte, b)
	vector := make([]byte, t)
	//Reconstruct each byte
	for idx := range secret {
		//Construct the linear system needed to be solved
		m := make([][]byte, t)
		for i := range m {
			m[i] = shares[i][idx]
			vector[i] = m[i][t-1]
			m[i][t-1] = 255
		}
		//Solve the system
		secret[idx], err = solve(m, vector, t)
	}
	return secret, err
}
