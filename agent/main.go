package main

import (
    "context"
    "fmt"
    "google.golang.org/grpc"
    "google.golang.org/grpc/reflection"
    "log"
    "net"
    pb "snivur/v0/proto"
)

type server struct{
    pb.UnimplementedGameServerManagerServer // Embed the unimplemented server
}

// StartServer starts the game server (this is just a placeholder)
func (s *server) StartServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerResponse, error) {
    // Logic to start the server (e.g., execute a shell command to start the game server)
    fmt.Printf("Starting server: %s at path: %s\n", req.Name, req.Path)
    return &pb.ServerResponse{
        Status:  "success",
        Message: fmt.Sprintf("Server %s started successfully", req.Name),
    }, nil
}

// StopServer stops the game server (this is just a placeholder)
func (s *server) StopServer(ctx context.Context, req *pb.ServerRequest) (*pb.ServerResponse, error) {
    // Logic to stop the server (e.g., terminate the server process)
    fmt.Printf("Stopping server: %s\n", req.Name)
    return &pb.ServerResponse{
        Status:  "success",
        Message: fmt.Sprintf("Server %s stopped successfully", req.Name),
    }, nil
}

// HealthCheck checks if the agent is alive
func (s *server) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
    return &pb.HealthCheckResponse{
        Alive: true, // Agent is alive
    }, nil
}

func main() {
    lis, err := net.Listen("tcp", ":50051")
    if err != nil {
        log.Fatalf("Failed to listen: %v", err)
    }

    grpcServer := grpc.NewServer()
    pb.RegisterGameServerManagerServer(grpcServer, &server{})

    // Register reflection service on gRPC server (optional, but useful for debugging)
    reflection.Register(grpcServer)

    fmt.Println("Agent gRPC server running on :50051")
    if err := grpcServer.Serve(lis); err != nil {
        log.Fatalf("Failed to serve: %v", err)
    }
}
