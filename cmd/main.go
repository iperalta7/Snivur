package main

import (
	"fmt"
	"log"
	"net"
	"context"
	"google.golang.org/grpc/reflection"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/credentials/insecure"
	pb "snivur/v0/proto" // Path to your generated pb package
	db "snivur/v0/db"    // Path to your db package
)

// Define the server struct that implements the gRPC service
type server struct {
	pb.UnimplementedGameServerManagerServer
}

func getClientIP(ctx context.Context) (string, error) {
    // Extract the client IP address from the context
    peer, ok := peer.FromContext(ctx)
    if !ok {
        return "", fmt.Errorf("failed to get peer from context")
    }
    
    // Convert the peer address to a string
    ip := peer.Addr.String()
    
    return ip, nil
}

// StartServer handles the gRPC request to start a game server
func (s *server) StartServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerResponse, error) {
    ip, err := getClientIP(ctx)
    if err != nil {
        log.Printf("Failed to get client IP: %v", err)
    }

    log.Printf("Starting server: %s at path: %s from client %s\n", req.Name, req.Path, ip)
    
	// Call the AddServer function in db.go to add a new server to the database
	err = db.AddServer(req.Name, "running", req.Path)
	if err != nil {
		return nil, fmt.Errorf("could not add server: %v", err)
	}

	// Return a response to the client
	return &pb.ServerResponse{
		Status:  "success",
		Message: fmt.Sprintf("Server %s started at %s", req.Name, req.Path),
	}, nil
}

// StopServer handles the gRPC request to stop a game server
func (s *server) StopServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerResponse, error) {
	// Call the UpdateServerStatus function in db.go to mark the server as stopped
	err := db.UpdateServerStatus(req.Name, "stopped")
	if err != nil {
		return nil, fmt.Errorf("could not stop server: %v", err)
	}

	// Return a response to the client
	return &pb.ServerResponse{
		Status:  "success",
		Message: fmt.Sprintf("Server %s stopped", req.Name),
	}, nil
}

// ListServers lists all the servers in the database
func (s *server) ListServers(ctx context.Context, req *pb.ListServersRequest) (*pb.ListServersResponse, error) {
    // Get the list of all servers from the database
    servers, err := db.GetServers()
    if err != nil {
        return nil, err
    }

    // Convert the database servers to the appropriate response format
    var serverList []*pb.Server
    for _, server := range servers {
        serverList = append(serverList, &pb.Server{
            Name:   server.Name,
            Status: server.Status,
            Path:   server.Path,
        })
    }

    // Return the list of servers
    return &pb.ListServersResponse{
        Servers: serverList,
    }, nil
}

// HealthCheck checks the health of a specific remote game server by its address
func (s *server) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	// Get the remote server's address from the request
	address := req.ServerAddress // Use the correct field name here

	// Dial the remote server to create a connection
	conn, err := grpc.DialContext(ctx, address, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("Dial failed: %v", err)
		return nil, fmt.Errorf("failed to connect to server: %v", err)
	}
	defer conn.Close() // Close the connection when done

	// Create a new client for the remote server
	client := pb.NewGameServerManagerClient(conn)

	// Call the HealthCheck method on the remote server
	resp, err := client.HealthCheck(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("could not perform health check on server at %v: %v", address, err)
	}

	// Return the health check response from the remote server
	return resp, nil
}


func main() {
	// Initialize the database (this is where you open the SQLite connection)
	err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Create a new gRPC server
	grpcServer := grpc.NewServer()

	// Register the GameServerManager service with the gRPC server
    pb.RegisterGameServerManagerServer(grpcServer, &server{})

	// Enable reflection for the server
	reflection.Register(grpcServer)

	// Set up the listener on port 50051
	lis, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatalf("Failed to listen on port 8080: %v", err)
	}

	fmt.Println("Server listening on port 8080...")

	// Start the gRPC server
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve gRPC: %v", err)
	}
}
